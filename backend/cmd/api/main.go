package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"roving/backend/internal/attendance"
	"roving/backend/internal/auth"
	"roving/backend/internal/chess"
	"roving/backend/internal/config"
	"roving/backend/internal/database"
	"roving/backend/internal/focus"
	"roving/backend/internal/groups"
	"roving/backend/internal/httpx"
	"roving/backend/internal/mailer"
	"roving/backend/internal/notes"
	"roving/backend/internal/notifications"
	"roving/backend/internal/reading"
	"roving/backend/internal/realtime"
	"roving/backend/internal/rooms"
	"roving/backend/internal/scheduling"
	"roving/backend/internal/stream"
	"roving/backend/internal/users"
)


func main() {
	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; using environment variables")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := database.Connect(dbCtx)
	if err != nil {
		log.Fatalf("PostgreSQL connection failed: %v", err)
	}
	defer db.Close()
	log.Println("PostgreSQL connection successful")

	// Mail: real Resend when a key is set, console otherwise.
	var mail mailer.Sender = mailer.LogMailer{}
	if cfg.ResendAPIKey != "" {
		mail = mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
	}

	userStore := users.NewStore(db)
	sessionStore := auth.NewSessionStore(db, cfg.SessionTTL, cfg.SessionIdleTTL)
	tokenStore := auth.NewEmailTokenStore(db)
	authHandler := auth.NewHandler(cfg, userStore, sessionStore, tokenStore, mail)
	requireAuth := authHandler.RequireAuth()

	groupsRepo := groups.NewRepository(db)
	groupsService := groups.NewService(groupsRepo, nil)
	groupsHandler := groups.NewHandler(groupsService, requireAuth)

	schedulingRepo := scheduling.NewRepository(db)
	schedulingSvc := scheduling.NewService(schedulingRepo, db)
	schedulingHandler := scheduling.NewHandler(schedulingSvc, requireAuth)

	roomsRepo := rooms.NewRepository(db)
	roomsSvc := rooms.NewService(roomsRepo, db)
	roomsHandler := rooms.NewHandler(roomsSvc, requireAuth)

	attendanceRepo := attendance.NewRepository(db)
	attendanceSvc := attendance.NewService(attendanceRepo, db)
	attendanceHandler := attendance.NewHandler(attendanceSvc, requireAuth)

	focusRepo := focus.NewRepository(db)
	focusSvc := focus.NewService(focusRepo, db)
	focusHandler := focus.NewHandler(focusSvc, requireAuth)

	// Notifications first: other modules produce through it.
	notificationsRepo := notifications.NewRepository(db)
	notificationsSvc := notifications.NewService(notificationsRepo, db)
	notificationsHandler := notifications.NewHandler(notificationsSvc, requireAuth)

	readingRepo := reading.NewRepository(db)
	readingSvc := reading.NewService(readingRepo, db, notificationsSvc)
	readingHandler := reading.NewHandler(readingSvc, requireAuth)

	notesRepo := notes.NewRepository(db)
	notesSvc := notes.NewService(notesRepo, db)
	notesHandler := notes.NewHandler(notesSvc, requireAuth)

	// Realtime (stage 1: connection foundation only). Chat and WebRTC are being
	// rebuilt on top of this hub in later stages. The session is re-checked every
	// couple of minutes so logging out also ends the socket.
	rtHub := realtime.NewHub(realtime.Config{
		Reauth: func(ctx context.Context, token string) (string, error) {
			uid, err := sessionStore.Lookup(ctx, token)
			if errors.Is(err, auth.ErrInvalidSession) {
				return "", realtime.ErrSessionInvalid
			}
			return uid, err
		},
	})

	// Live room stream (peer-to-peer WebRTC, signalled over the hub above).
	// Authorization is the rooms service's own: a user can join only if the rooms
	// module says they may enter, and only the GROUP OWNER is the publisher.
	// To let room hosts or admins publish too, widen the canPublish line.
	streamReg := stream.NewRegistry()
	stream.Register(rtHub, streamReg, func(ctx context.Context, userID, groupID, roomID string) (stream.Role, error) {
		detail, err := roomsSvc.Get(ctx, userID, groupID, roomID)
		if err != nil {
			return "", err
		}
		if detail.ArchivedAt != nil {
			return "", errors.New("room is archived")
		}
		if !detail.CanEnter {
			return "", errors.New("you cannot enter this room")
		}
	
		// The group owner is the only publisher. Everything else is mic-only.
		// (Optionally: explicit room hosts of a restricted room can publish too —
		// uncomment the second clause if that's desired.)
		canPublish := detail.MyRole == rooms.GroupRoleOwner
		// || detail.MyRoomRole == rooms.RoomRoleHost
	
		if canPublish {
			return stream.RoleHost, nil
		}
		return stream.RoleViewer, nil
	})
	realtime.RegisterChat(rtHub, streamReg) // once, here: not inside the resolver

	// Chess: a separate, game-scoped WebSocket hub plus the REST service.
	chessRepo := chess.NewRepository(db)
	chessEngine := chess.NewEngine()
	chessHub := chess.NewHub()
	chessSvc := chess.NewService(chessRepo, db, chessEngine)
	chessSvc.SetPublisher(chessHub)
	chessHandler := chess.NewHandler(chessSvc, chessHub, requireAuth, cfg.FrontendOrigin)

	// Background workers.
	go schedulingSvc.RunGenerator(ctx, time.Hour)
	go notificationsSvc.RunWorker(ctx, time.Minute)
	go sessionStore.RunCleanup(ctx, time.Hour)
	go chessSvc.RunExpiry(ctx, 30*time.Second) // delete games nobody joined within chess.PendingTTL

	router := gin.New()
	_ = router.SetTrustedProxies(nil) // trust no proxy headers until you deploy behind one
	router.Use(gin.Logger(), gin.Recovery())
	router.Use(httpx.CORS(cfg.FrontendOrigin), httpx.RequireSameOrigin(cfg.FrontendOrigin))

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/health/db", func(c *gin.Context) {
		pctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		if err := db.Ping(pctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "connected"})
	})

	api := router.Group("/api/v1")
	authHandler.Routes(api)
	groupsHandler.Routes(api)
	schedulingHandler.Routes(api)
	roomsHandler.Routes(api)
	attendanceHandler.Routes(api)
	focusHandler.Routes(api)
	notificationsHandler.Routes(api)
	readingHandler.Routes(api)
	notesHandler.Routes(api)
	realtime.Routes(api, rtHub, requireAuth, auth.UserID, auth.CookieName, cfg.FrontendOrigin)
	chessHandler.Routes(api)

	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second, // does not apply to upgraded WebSockets
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		// Server.Shutdown does not close hijacked connections, so close them here.
		hubCtx, hubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		rtHub.Shutdown(hubCtx)
		hubCancel()
		chessHub.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("API listening on http://%s", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}


// package main

// import (
// 	"context"
// 	"errors"
// 	"log"
// 	"net/http"
// 	"os"
// 	"os/signal"
// 	"syscall"
// 	"time"

// 	"github.com/gin-gonic/gin"
// 	"github.com/joho/godotenv"

// 	"roving/backend/internal/attendance"
// 	"roving/backend/internal/auth"
// 	"roving/backend/internal/chess"
// 	"roving/backend/internal/config"
// 	"roving/backend/internal/database"
// 	"roving/backend/internal/focus"
// 	"roving/backend/internal/groups"
// 	"roving/backend/internal/httpx"
// 	"roving/backend/internal/mailer"
// 	"roving/backend/internal/notes"
// 	"roving/backend/internal/notifications"
// 	"roving/backend/internal/reading"
// 	"roving/backend/internal/realtime"
// 	"roving/backend/internal/rooms"
// 	"roving/backend/internal/scheduling"
// 	"roving/backend/internal/stream"
// 	"roving/backend/internal/users"
// )


// func main() {
// 	if err := godotenv.Load(); err != nil {
// 		log.Println(".env not found; using environment variables")
// 	}

// 	cfg, err := config.Load()
// 	if err != nil {
// 		log.Fatalf("config: %v", err)
// 	}
// 	if cfg.Env == "production" {
// 		gin.SetMode(gin.ReleaseMode)
// 	}

// 	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
// 	defer stop()

// 	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
// 	defer cancel()
// 	db, err := database.Connect(dbCtx)
// 	if err != nil {
// 		log.Fatalf("PostgreSQL connection failed: %v", err)
// 	}
// 	defer db.Close()
// 	log.Println("PostgreSQL connection successful")

// 	// Mail: real Resend when a key is set, console otherwise.
// 	var mail mailer.Sender = mailer.LogMailer{}
// 	if cfg.ResendAPIKey != "" {
// 		mail = mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
// 	}

// 	userStore := users.NewStore(db)
// 	sessionStore := auth.NewSessionStore(db, cfg.SessionTTL, cfg.SessionIdleTTL)
// 	tokenStore := auth.NewEmailTokenStore(db)
// 	authHandler := auth.NewHandler(cfg, userStore, sessionStore, tokenStore, mail)
// 	requireAuth := authHandler.RequireAuth()

// 	groupsRepo := groups.NewRepository(db)
// 	groupsService := groups.NewService(groupsRepo, nil)
// 	groupsHandler := groups.NewHandler(groupsService, requireAuth)

// 	schedulingRepo := scheduling.NewRepository(db)
// 	schedulingSvc := scheduling.NewService(schedulingRepo, db)
// 	schedulingHandler := scheduling.NewHandler(schedulingSvc, requireAuth)

// 	roomsRepo := rooms.NewRepository(db)
// 	roomsSvc := rooms.NewService(roomsRepo, db)
// 	roomsHandler := rooms.NewHandler(roomsSvc, requireAuth)

// 	attendanceRepo := attendance.NewRepository(db)
// 	attendanceSvc := attendance.NewService(attendanceRepo, db)
// 	attendanceHandler := attendance.NewHandler(attendanceSvc, requireAuth)

// 	focusRepo := focus.NewRepository(db)
// 	focusSvc := focus.NewService(focusRepo, db)
// 	focusHandler := focus.NewHandler(focusSvc, requireAuth)

// 	// Notifications first: other modules produce through it.
// 	notificationsRepo := notifications.NewRepository(db)
// 	notificationsSvc := notifications.NewService(notificationsRepo, db)
// 	notificationsHandler := notifications.NewHandler(notificationsSvc, requireAuth)

// 	readingRepo := reading.NewRepository(db)
// 	readingSvc := reading.NewService(readingRepo, db, notificationsSvc)
// 	readingHandler := reading.NewHandler(readingSvc, requireAuth)

// 	notesRepo := notes.NewRepository(db)
// 	notesSvc := notes.NewService(notesRepo, db)
// 	notesHandler := notes.NewHandler(notesSvc, requireAuth)

// 	// Realtime (stage 1: connection foundation only). Chat and WebRTC are being
// 	// rebuilt on top of this hub in later stages. The session is re-checked every
// 	// couple of minutes so logging out also ends the socket.
// 	rtHub := realtime.NewHub(realtime.Config{
// 		Reauth: func(ctx context.Context, token string) (string, error) {
// 			uid, err := sessionStore.Lookup(ctx, token)
// 			if errors.Is(err, auth.ErrInvalidSession) {
// 				return "", realtime.ErrSessionInvalid
// 			}
// 			return uid, err
// 		},
// 	})

// 	// Live room stream (peer-to-peer WebRTC, signalled over the hub above).
// 	// Authorization is the rooms service's own: a user can join only if the rooms
// 	// module says they may enter, and only the GROUP OWNER is the publisher.
// 	// To let room hosts or admins publish too, widen the canPublish line.
// 	streamReg := stream.NewRegistry()
// 	stream.Register(rtHub, streamReg, func(ctx context.Context, userID, groupID, roomID string) (stream.Role, error) {
// 		detail, err := roomsSvc.Get(ctx, userID, groupID, roomID)
// 		if err != nil {
// 			return "", err
// 		}
// 		if detail.ArchivedAt != nil {
// 			return "", errors.New("room is archived")
// 		}
// 		if !detail.CanEnter {
// 			return "", errors.New("you cannot enter this room")
// 		}
	
// 		// The group owner is the only publisher. Everything else is mic-only.
// 		// (Optionally: explicit room hosts of a restricted room can publish too —
// 		// uncomment the second clause if that's desired.)
// 		canPublish := detail.MyRole == rooms.GroupRoleOwner
// 		// || detail.MyRoomRole == rooms.RoomRoleHost
	
// 		if canPublish {
// 			return stream.RoleHost, nil
// 		}
// 		return stream.RoleViewer, nil
// 	})
// 	realtime.RegisterChat(rtHub, streamReg) // once, here: not inside the resolver

// 	// Chess: a separate, game-scoped WebSocket hub plus the REST service.
// 	chessRepo := chess.NewRepository(db)
// 	chessEngine := chess.NewEngine()
// 	chessHub := chess.NewHub()
// 	chessSvc := chess.NewService(chessRepo, db, chessEngine)
// 	chessSvc.SetPublisher(chessHub)
// 	chessHandler := chess.NewHandler(chessSvc, chessHub, requireAuth, cfg.FrontendOrigin)

// 	// Background workers.
// 	go schedulingSvc.RunGenerator(ctx, time.Hour)
// 	go notificationsSvc.RunWorker(ctx, time.Minute)
// 	go sessionStore.RunCleanup(ctx, time.Hour)

// 	router := gin.New()
// 	_ = router.SetTrustedProxies(nil) // trust no proxy headers until you deploy behind one
// 	router.Use(gin.Logger(), gin.Recovery())
// 	router.Use(httpx.CORS(cfg.FrontendOrigin), httpx.RequireSameOrigin(cfg.FrontendOrigin))

// 	router.GET("/health", func(c *gin.Context) {
// 		c.JSON(http.StatusOK, gin.H{"status": "ok"})
// 	})
// 	router.GET("/health/db", func(c *gin.Context) {
// 		pctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
// 		defer cancel()
// 		if err := db.Ping(pctx); err != nil {
// 			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
// 			return
// 		}
// 		c.JSON(http.StatusOK, gin.H{"status": "connected"})
// 	})

// 	api := router.Group("/api/v1")
// 	authHandler.Routes(api)
// 	groupsHandler.Routes(api)
// 	schedulingHandler.Routes(api)
// 	roomsHandler.Routes(api)
// 	attendanceHandler.Routes(api)
// 	focusHandler.Routes(api)
// 	notificationsHandler.Routes(api)
// 	readingHandler.Routes(api)
// 	notesHandler.Routes(api)
// 	realtime.Routes(api, rtHub, requireAuth, auth.UserID, auth.CookieName, cfg.FrontendOrigin)
// 	chessHandler.Routes(api)

// 	addr := os.Getenv("API_ADDR")
// 	if addr == "" {
// 		addr = "127.0.0.1:8080"
// 	}
// 	server := &http.Server{
// 		Addr:              addr,
// 		Handler:           router,
// 		ReadHeaderTimeout: 5 * time.Second,
// 		ReadTimeout:       10 * time.Second,
// 		WriteTimeout:      20 * time.Second, // does not apply to upgraded WebSockets
// 		IdleTimeout:       60 * time.Second,
// 	}

// 	go func() {
// 		<-ctx.Done()
// 		// Server.Shutdown does not close hijacked connections, so close them here.
// 		hubCtx, hubCancel := context.WithTimeout(context.Background(), 5*time.Second)
// 		rtHub.Shutdown(hubCtx)
// 		hubCancel()
// 		chessHub.Shutdown()
// 		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
// 		defer cancel()
// 		_ = server.Shutdown(shutdownCtx)
// 	}()

// 	log.Printf("API listening on http://%s", addr)
// 	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
// 		log.Fatal(err)
// 	}
// }


// package main

// import (
// 	"context"
// 	"errors"
// 	"log"
// 	"net/http"
// 	"os"
// 	"os/signal"
// 	"syscall"
// 	"time"

// 	"github.com/gin-gonic/gin"
// 	"github.com/joho/godotenv"

// 	"roving/backend/internal/attendance"
// 	"roving/backend/internal/auth"
// 	"roving/backend/internal/chess"
// 	"roving/backend/internal/config"
// 	"roving/backend/internal/database"
// 	"roving/backend/internal/focus"
// 	"roving/backend/internal/groups"
// 	"roving/backend/internal/httpx"
// 	"roving/backend/internal/mailer"
// 	"roving/backend/internal/notes"
// 	"roving/backend/internal/notifications"
// 	"roving/backend/internal/reading"
// 	"roving/backend/internal/realtime"
// 	"roving/backend/internal/rooms"
// 	"roving/backend/internal/scheduling"
// 	"roving/backend/internal/stream"
// 	"roving/backend/internal/users"
// )

// func main() {
// 	if err := godotenv.Load(); err != nil {
// 		log.Println(".env not found; using environment variables")
// 	}

// 	cfg, err := config.Load()
// 	if err != nil {
// 		log.Fatalf("config: %v", err)
// 	}
// 	if cfg.Env == "production" {
// 		gin.SetMode(gin.ReleaseMode)
// 	}

// 	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
// 	defer stop()

// 	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
// 	defer cancel()
// 	db, err := database.Connect(dbCtx)
// 	if err != nil {
// 		log.Fatalf("PostgreSQL connection failed: %v", err)
// 	}
// 	defer db.Close()
// 	log.Println("PostgreSQL connection successful")

// 	var mail mailer.Sender = mailer.LogMailer{}
// 	if cfg.ResendAPIKey != "" {
// 		mail = mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
// 	}

// 	userStore := users.NewStore(db)
// 	sessionStore := auth.NewSessionStore(db, cfg.SessionTTL, cfg.SessionIdleTTL)
// 	tokenStore := auth.NewEmailTokenStore(db)
// 	authHandler := auth.NewHandler(cfg, userStore, sessionStore, tokenStore, mail)
// 	requireAuth := authHandler.RequireAuth()

// 	groupsRepo := groups.NewRepository(db)
// 	groupsService := groups.NewService(groupsRepo, nil)
// 	groupsHandler := groups.NewHandler(groupsService, requireAuth)

// 	schedulingRepo := scheduling.NewRepository(db)
// 	schedulingSvc := scheduling.NewService(schedulingRepo, db)
// 	schedulingHandler := scheduling.NewHandler(schedulingSvc, requireAuth)

// 	roomsRepo := rooms.NewRepository(db)
// 	roomsSvc := rooms.NewService(roomsRepo, db)
// 	roomsHandler := rooms.NewHandler(roomsSvc, requireAuth)

// 	attendanceRepo := attendance.NewRepository(db)
// 	attendanceSvc := attendance.NewService(attendanceRepo, db)
// 	attendanceHandler := attendance.NewHandler(attendanceSvc, requireAuth)

// 	focusRepo := focus.NewRepository(db)
// 	focusSvc := focus.NewService(focusRepo, db)
// 	focusHandler := focus.NewHandler(focusSvc, requireAuth)

// 	notificationsRepo := notifications.NewRepository(db)
// 	notificationsSvc := notifications.NewService(notificationsRepo, db)
// 	notificationsHandler := notifications.NewHandler(notificationsSvc, requireAuth)

// 	readingRepo := reading.NewRepository(db)
// 	readingSvc := reading.NewService(readingRepo, db, notificationsSvc)
// 	readingHandler := reading.NewHandler(readingSvc, requireAuth)

// 	notesRepo := notes.NewRepository(db)
// 	notesSvc := notes.NewService(notesRepo, db)
// 	notesHandler := notes.NewHandler(notesSvc, requireAuth)

// 	// Realtime hub. The session is re-checked every couple of minutes so logging
// 	// out also ends the socket.
// 	rtHub := realtime.NewHub(realtime.Config{
// 		Reauth: func(ctx context.Context, token string) (string, error) {
// 			uid, err := sessionStore.Lookup(ctx, token)
// 			if errors.Is(err, auth.ErrInvalidSession) {
// 				return "", realtime.ErrSessionInvalid
// 			}
// 			return uid, err
// 		},
// 	})

// 	// Stream (WebRTC signalling) + chat, wired ONCE, outside any closure.
// 	streamReg := stream.NewRegistry()

// 	stream.Register(rtHub, streamReg, func(ctx context.Context, userID, groupID, roomID string) (stream.Role, string, error) {
// 		d, err := roomsSvc.Get(ctx, userID, groupID, roomID)
// 		if err != nil {
// 			return "", "", err
// 		}
// 		if d == nil || d.ArchivedAt != nil || !d.CanEnter {
// 			return "", "", errors.New("not allowed")
// 		}
// 		var name string
// 		_ = db.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1::uuid`, userID).Scan(&name)
// 		if name == "" {
// 			name = "Member"
// 		}
// 		return stream.RoleFor(d.StreamRole()), name, nil
// 	})

// 	realtime.RegisterChat(rtHub, streamReg)
// 	rtHub.OnDisconnect(func(c *realtime.Client) { streamReg.RemoveConn(c.ID, rtHub) })

// 	chessRepo := chess.NewRepository(db)
// 	chessEngine := chess.NewEngine()
// 	chessHub := chess.NewHub()
// 	chessSvc := chess.NewService(chessRepo, db, chessEngine)
// 	chessSvc.SetPublisher(chessHub)
// 	chessHandler := chess.NewHandler(chessSvc, chessHub, requireAuth, cfg.FrontendOrigin)

// 	go schedulingSvc.RunGenerator(ctx, time.Hour)
// 	go notificationsSvc.RunWorker(ctx, time.Minute)
// 	go sessionStore.RunCleanup(ctx, time.Hour)

// 	router := gin.New()
// 	_ = router.SetTrustedProxies(nil)
// 	router.Use(gin.Logger(), gin.Recovery())
// 	router.Use(httpx.CORS(cfg.FrontendOrigin), httpx.RequireSameOrigin(cfg.FrontendOrigin))

// 	router.GET("/health", func(c *gin.Context) {
// 		c.JSON(http.StatusOK, gin.H{"status": "ok"})
// 	})
// 	router.GET("/health/db", func(c *gin.Context) {
// 		pctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
// 		defer cancel()
// 		if err := db.Ping(pctx); err != nil {
// 			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
// 			return
// 		}
// 		c.JSON(http.StatusOK, gin.H{"status": "connected"})
// 	})

// 	api := router.Group("/api/v1")
// 	authHandler.Routes(api)
// 	groupsHandler.Routes(api)
// 	schedulingHandler.Routes(api)
// 	roomsHandler.Routes(api)
// 	attendanceHandler.Routes(api)
// 	focusHandler.Routes(api)
// 	notificationsHandler.Routes(api)
// 	readingHandler.Routes(api)
// 	notesHandler.Routes(api)
// 	realtime.Routes(api, rtHub, requireAuth, auth.UserID, auth.CookieName, cfg.FrontendOrigin)
// 	chessHandler.Routes(api)

// 	addr := os.Getenv("API_ADDR")
// 	if addr == "" {
// 		addr = "127.0.0.1:8080"
// 	}
// 	server := &http.Server{
// 		Addr:              addr,
// 		Handler:           router,
// 		ReadHeaderTimeout: 5 * time.Second,
// 		ReadTimeout:       10 * time.Second,
// 		WriteTimeout:      20 * time.Second,
// 		IdleTimeout:       60 * time.Second,
// 	}

// 	go func() {
// 		<-ctx.Done()
// 		hubCtx, hubCancel := context.WithTimeout(context.Background(), 5*time.Second)
// 		rtHub.Shutdown(hubCtx)
// 		hubCancel()
// 		chessHub.Shutdown()
// 		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
// 		defer cancel()
// 		_ = server.Shutdown(shutdownCtx)
// 	}()

// 	log.Printf("API listening on http://%s", addr)
// 	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
// 		log.Fatal(err)
// 	}
// }

// // package main

// // import (
// // 	"context"
// // 	"errors"
// // 	"log"
// // 	"net/http"
// // 	"os"
// // 	"os/signal"
// // 	"syscall"
// // 	"time"

// // 	"github.com/gin-gonic/gin"
// // 	"github.com/joho/godotenv"

// // 	"roving/backend/internal/attendance"
// // 	"roving/backend/internal/auth"
// // 	"roving/backend/internal/chess"
// // 	"roving/backend/internal/config"
// // 	"roving/backend/internal/database"
// // 	"roving/backend/internal/focus"
// // 	"roving/backend/internal/groups"
// // 	"roving/backend/internal/httpx"
// // 	"roving/backend/internal/mailer"
// // 	"roving/backend/internal/notes"
// // 	"roving/backend/internal/notifications"
// // 	"roving/backend/internal/reading"
// // 	"roving/backend/internal/realtime"
// // 	"roving/backend/internal/rooms"
// // 	"roving/backend/internal/scheduling"
// // 	"roving/backend/internal/users"
// // 	"roving/backend/internal/stream"
	
// // )

// // func main() {
// // 	if err := godotenv.Load(); err != nil {
// // 		log.Println(".env not found; using environment variables")
// // 	}

// // 	cfg, err := config.Load()
// // 	if err != nil {
// // 		log.Fatalf("config: %v", err)
// // 	}
// // 	if cfg.Env == "production" {
// // 		gin.SetMode(gin.ReleaseMode)
// // 	}

// // 	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
// // 	defer stop()

// // 	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
// // 	defer cancel()
// // 	db, err := database.Connect(dbCtx)
// // 	if err != nil {
// // 		log.Fatalf("PostgreSQL connection failed: %v", err)
// // 	}
// // 	defer db.Close()
// // 	log.Println("PostgreSQL connection successful")

// // 	// Mail: real Resend when a key is set, console otherwise.
// // 	var mail mailer.Sender = mailer.LogMailer{}
// // 	if cfg.ResendAPIKey != "" {
// // 		mail = mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
// // 	}

// // 	userStore := users.NewStore(db)
// // 	sessionStore := auth.NewSessionStore(db, cfg.SessionTTL, cfg.SessionIdleTTL)
// // 	tokenStore := auth.NewEmailTokenStore(db)
// // 	authHandler := auth.NewHandler(cfg, userStore, sessionStore, tokenStore, mail)
// // 	requireAuth := authHandler.RequireAuth()

// // 	groupsRepo := groups.NewRepository(db)
// // 	groupsService := groups.NewService(groupsRepo, nil)
// // 	groupsHandler := groups.NewHandler(groupsService, requireAuth)

// // 	schedulingRepo := scheduling.NewRepository(db)
// // 	schedulingSvc := scheduling.NewService(schedulingRepo, db)
// // 	schedulingHandler := scheduling.NewHandler(schedulingSvc, requireAuth)

// // 	roomsRepo := rooms.NewRepository(db)
// // 	roomsSvc := rooms.NewService(roomsRepo, db)
// // 	roomsHandler := rooms.NewHandler(roomsSvc, requireAuth)

// // 	attendanceRepo := attendance.NewRepository(db)
// // 	attendanceSvc := attendance.NewService(attendanceRepo, db)
// // 	attendanceHandler := attendance.NewHandler(attendanceSvc, requireAuth)

// // 	focusRepo := focus.NewRepository(db)
// // 	focusSvc := focus.NewService(focusRepo, db)
// // 	focusHandler := focus.NewHandler(focusSvc, requireAuth)

// // 	// Notifications first: other modules produce through it.
// // 	notificationsRepo := notifications.NewRepository(db)
// // 	notificationsSvc := notifications.NewService(notificationsRepo, db)
// // 	notificationsHandler := notifications.NewHandler(notificationsSvc, requireAuth)

// // 	readingRepo := reading.NewRepository(db)
// // 	readingSvc := reading.NewService(readingRepo, db, notificationsSvc)
// // 	readingHandler := reading.NewHandler(readingSvc, requireAuth)

// // 	notesRepo := notes.NewRepository(db)
// // 	notesSvc := notes.NewService(notesRepo, db)
// // 	notesHandler := notes.NewHandler(notesSvc, requireAuth)

// // 	// Realtime (stage 1: connection foundation only). Chat and WebRTC are being
// // 	// rebuilt on top of this hub in later stages. The session is re-checked every
// // 	// couple of minutes so logging out also ends the socket.
// // 	rtHub := realtime.NewHub(realtime.Config{
// // 		Reauth: func(ctx context.Context, token string) (string, error) {
// // 			uid, err := sessionStore.Lookup(ctx, token)
// // 			if errors.Is(err, auth.ErrInvalidSession) {
// // 				return "", realtime.ErrSessionInvalid
// // 			}
// // 			return uid, err
// // 		},
// // 	})



// // 	// NEW: stream package wiring
// // 	// streamReg := stream.NewRegistry()
// // 	streamReg := stream.NewRegistry()

// // 	stream.Register(rtHub, streamReg, func(ctx context.Context, userID, groupID, roomID string) (stream.Role, error) {
// // 		realtime.RegisterChat(rtHub, streamReg)

// // 		detail, err := roomsSvc.Get(ctx, userID, groupID, roomID)
// //     if err != nil {
// //         return "", err
// //     }
// //     if detail == nil {
// //         return "", errors.New("room not found")
// //     }
// //     if detail.ArchivedAt != nil {
// //         return "", errors.New("room is archived")
// //     }
// //     if !detail.CanEnter {
// //         return "", errors.New("you cannot enter this room")
// //     }
// //     return stream.RoleFor(detail.MyRoomRole.String()), nil
// // 	})



// // 	stream.Register(hub, streamReg, func(ctx context.Context, userID, groupID, roomID string) (stream.Role, error) {
// //     room, err := roomsSvc.Get(ctx, userID, groupID, roomID) // <- your real method
// //     if err != nil {
// //         return "", err
// //     }
// //     if !room.CanEnter {
// //         return "", errors.New("not allowed")
// //     }
// //     // Host = whoever your rooms package says hosts the room. If you want
// //     // "only the GROUP OWNER streams", return stream.RoleHost only when
// //     // userID == group owner id, and stream.RoleViewer for everyone else.
// //     return stream.RoleFor(string(room.MyRoomRole)), nil
// // 	})
	
// // 	realtime.RegisterChat(hub, streamReg)
// // 	hub.OnDisconnect(func(c *realtime.Client) { streamReg.RemoveConn(c.ID, hub) })

// // 	// Chess: a separate, game-scoped WebSocket hub plus the REST service.
// // 	chessRepo := chess.NewRepository(db)
// // 	chessEngine := chess.NewEngine()
// // 	chessHub := chess.NewHub()
// // 	chessSvc := chess.NewService(chessRepo, db, chessEngine)
// // 	chessSvc.SetPublisher(chessHub)
// // 	chessHandler := chess.NewHandler(chessSvc, chessHub, requireAuth, cfg.FrontendOrigin)

// // 	// Background workers.
// // 	go schedulingSvc.RunGenerator(ctx, time.Hour)
// // 	go notificationsSvc.RunWorker(ctx, time.Minute)
// // 	go sessionStore.RunCleanup(ctx, time.Hour)

// // 	router := gin.New()
// // 	_ = router.SetTrustedProxies(nil) // trust no proxy headers until you deploy behind one
// // 	router.Use(gin.Logger(), gin.Recovery())
// // 	router.Use(httpx.CORS(cfg.FrontendOrigin), httpx.RequireSameOrigin(cfg.FrontendOrigin))

// // 	router.GET("/health", func(c *gin.Context) {
// // 		c.JSON(http.StatusOK, gin.H{"status": "ok"})
// // 	})
// // 	router.GET("/health/db", func(c *gin.Context) {
// // 		pctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
// // 		defer cancel()
// // 		if err := db.Ping(pctx); err != nil {
// // 			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
// // 			return
// // 		}
// // 		c.JSON(http.StatusOK, gin.H{"status": "connected"})
// // 	})

// // 	api := router.Group("/api/v1")
// // 	authHandler.Routes(api)
// // 	groupsHandler.Routes(api)
// // 	schedulingHandler.Routes(api)
// // 	roomsHandler.Routes(api)
// // 	attendanceHandler.Routes(api)
// // 	focusHandler.Routes(api)
// // 	notificationsHandler.Routes(api)
// // 	readingHandler.Routes(api)
// // 	notesHandler.Routes(api)
// // 	realtime.Routes(api, rtHub, requireAuth, auth.UserID, auth.CookieName, cfg.FrontendOrigin)
// // 	chessHandler.Routes(api)

// // 	addr := os.Getenv("API_ADDR")
// // 	if addr == "" {
// // 		addr = "127.0.0.1:8080"
// // 	}
// // 	server := &http.Server{
// // 		Addr:              addr,
// // 		Handler:           router,
// // 		ReadHeaderTimeout: 5 * time.Second,
// // 		ReadTimeout:       10 * time.Second,
// // 		WriteTimeout:      20 * time.Second, // does not apply to upgraded WebSockets
// // 		IdleTimeout:       60 * time.Second,
// // 	}

// // 	go func() {
// // 		<-ctx.Done()
// // 		// Server.Shutdown does not close hijacked connections, so close them here.
// // 		hubCtx, hubCancel := context.WithTimeout(context.Background(), 5*time.Second)
// // 		rtHub.Shutdown(hubCtx)
// // 		hubCancel()
// // 		chessHub.Shutdown()
// // 		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
// // 		defer cancel()
// // 		_ = server.Shutdown(shutdownCtx)
// // 	}()

// // 	log.Printf("API listening on http://%s", addr)
// // 	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
// // 		log.Fatal(err)
// // 	}
// // }

