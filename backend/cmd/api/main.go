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
	"roving/backend/internal/chat"
	"roving/backend/internal/config"
	"roving/backend/internal/database"
	"roving/backend/internal/focus"
	"roving/backend/internal/groups"
	"roving/backend/internal/httpx"
	"roving/backend/internal/mailer"
	"roving/backend/internal/notes"
	"roving/backend/internal/notifications"
	"roving/backend/internal/reading"
	"roving/backend/internal/rooms"
	"roving/backend/internal/scheduling"
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

	chatRepo := chat.NewRepository(db)
	chatSvc := chat.NewService(chatRepo, db)
	chatHandler := chat.NewHandler(chatSvc, requireAuth)
	// When the WebSocket hub exists: chatSvc.SetPublisher(hub)

	// Background workers.
	go schedulingSvc.RunGenerator(ctx, time.Hour)
	go notificationsSvc.RunWorker(ctx, time.Minute)
	go sessionStore.RunCleanup(ctx, time.Hour)

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
	chatHandler.Routes(api)

	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
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
// 	"roving/backend/internal/config"
// 	"roving/backend/internal/database"
// 	"roving/backend/internal/focus"
// 	"roving/backend/internal/groups"
// 	"roving/backend/internal/httpx"
// 	"roving/backend/internal/mailer"
// 	"roving/backend/internal/notes"
// 	"roving/backend/internal/notifications"
// 	"roving/backend/internal/reading"
// 	"roving/backend/internal/rooms"
// 	"roving/backend/internal/scheduling"
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
// 	// "github.com/jackc/pgx/v5"
// 	"github.com/joho/godotenv"

// 	"roving/backend/internal/auth"
// 	"roving/backend/internal/config"
// 	"roving/backend/internal/database"
// 	"roving/backend/internal/groups"
// 	"roving/backend/internal/httpx"
// 	"roving/backend/internal/mailer"
// 	"roving/backend/internal/users"
//     "roving/backend/internal/scheduling"
//     "roving/backend/internal/rooms"
//     "roving/backend/internal/attendance"
//     "roving/backend/internal/focus"



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

// 	// groupsService := groups.NewService(db, noopPauser{})
// 	// groupsHandler := groups.NewHandler(groupsService, authHandler.RequireAuth())

// 	groupsRepo := groups.NewRepository(db)
// 	// groupsService := groups.NewService(groupsRepo, groups.noopPauser{})
// 	groupsService := groups.NewService(groupsRepo, nil)
// 	groupsHandler := groups.NewHandler(groupsService, authHandler.RequireAuth())





// 	schedulingRepo := scheduling.NewRepository(db)
// 	schedulingSvc := scheduling.NewService(schedulingRepo, db)
// 	schedulingHandler := scheduling.NewHandler(schedulingSvc, authHandler.RequireAuth())
	





// 	// After scheduling wiring (order doesn't matter here):
// 	roomsRepo := rooms.NewRepository(db)
// 	roomsSvc := rooms.NewService(roomsRepo, db)
// 	roomsHandler := rooms.NewHandler(roomsSvc, authHandler.RequireAuth())




// 	attendanceRepo := attendance.NewRepository(db)
// 	attendanceSvc := attendance.NewService(attendanceRepo, db)
// 	attendanceHandler := attendance.NewHandler(attendanceSvc, authHandler.RequireAuth())


// 	focusRepo := focus.NewRepository(db)
// 	focusSvc := focus.NewService(focusRepo, db)
// 	focusHandler := focus.NewHandler(focusSvc, authHandler.RequireAuth())



// 	// 3. START the rolling generator (runs in background)
// 	go schedulingSvc.RunGenerator(ctx, time.Hour)
	


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
// 		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
// 		defer cancel()
// 		_ = server.Shutdown(shutdownCtx)
// 	}()

// 	log.Printf("API listening on http://%s", addr)
// 	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
// 		log.Fatal(err)
// 	}
// }

// // noopPauser is the archive hook until the scheduling module exists. It keeps
// // the groups archive transaction atomic without importing a future package.
// // type noopPauser struct{}

// // func (noopPauser) PauseGroupActivity(_ context.Context, _ pgx.Tx, _ string) error {
// // 	return nil
// // }
