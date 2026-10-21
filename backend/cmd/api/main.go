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
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"

	"study-platform/backend/internal/auth"
	"study-platform/backend/internal/config"
	"study-platform/backend/internal/database"
	"study-platform/backend/internal/groups"
	"study-platform/backend/internal/httpx"
	"study-platform/backend/internal/mailer"
	"study-platform/backend/internal/users"
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

	groupsService := groups.NewService(db, noopPauser{})
	groupsHandler := groups.NewHandler(groupsService, authHandler.RequireAuth())

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

// noopPauser is the archive hook until the scheduling module exists. It keeps
// the groups archive transaction atomic without importing a future package.
type noopPauser struct{}

func (noopPauser) PauseGroupActivity(_ context.Context, _ pgx.Tx, _ string) error {
	return nil
}
