package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"study-platform/backend/internal/config"
	"study-platform/backend/internal/httpx"
	"study-platform/backend/internal/mailer"
	"study-platform/backend/internal/users"
)

const (
	CookieName = "rovingr_session"
	userIDKey  = "userID"
	verifyTTL  = 24 * time.Hour
	resetTTL   = time.Hour
)

type Handler struct {
	cfg      *config.Config
	users    *users.Store
	sessions *SessionStore
	tokens   *EmailTokenStore
	mailer   mailer.Sender
}

func NewHandler(cfg *config.Config, u *users.Store, s *SessionStore, t *EmailTokenStore, m mailer.Sender) *Handler {
	return &Handler{cfg: cfg, users: u, sessions: s, tokens: t, mailer: m}
}

func (h *Handler) Routes(rg *gin.RouterGroup) {
	g := rg.Group("/auth", httpx.NoStore())
	g.POST("/register", httpx.RateLimit(20*time.Second, 5), h.register)
	g.POST("/login", httpx.RateLimit(12*time.Second, 5), h.login)
	g.POST("/logout", h.logout)
	g.GET("/me", h.RequireAuth(), h.me)
	g.GET("/verify-email", h.verifyEmailPage)
	g.POST("/verify-email", httpx.RateLimit(10*time.Second, 10), h.verifyEmail)
	g.POST("/resend-verification", h.RequireAuth(), httpx.RateLimit(time.Minute, 3), h.resendVerification)
	g.POST("/forgot-password", httpx.RateLimit(30*time.Second, 3), h.forgotPassword)
	g.POST("/reset-password", httpx.RateLimit(10*time.Second, 10), h.resetPassword)
}

// ---------- middleware ----------

func (h *Handler) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(CookieName)
		if err != nil || raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		uid, err := h.sessions.Lookup(c.Request.Context(), raw)
		if err != nil {
			if !errors.Is(err, ErrInvalidSession) {
				log.Printf("session lookup: %v", err)
			}
			h.setCookie(c, "", -1)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		c.Set(userIDKey, uid)
		c.Next()
	}
}

// UserID returns the authenticated user's ID inside handlers behind RequireAuth.
func UserID(c *gin.Context) string { return c.GetString(userIDKey) }

// ---------- handlers ----------

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) register(c *gin.Context) {
	var req credentials
	if !bind(c, &req) {
		return
	}
	email, ok := validateEmail(req.Email)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enter a valid email address"})
		return
	}
	if err := ValidatePassword(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		h.fail(c, err)
		return
	}
	u, err := h.users.Create(c.Request.Context(), email, hash)
	if errors.Is(err, users.ErrEmailTaken) {
		c.JSON(http.StatusConflict, gin.H{"error": "could not create an account with these details"})
		return
	}
	if err != nil {
		h.fail(c, err)
		return
	}

	h.sendTokenMail(u.ID, u.Email, PurposeVerifyEmail, verifyTTL, "/verify-email",
		"Confirm your Rovingr email", "Welcome to Rovingr! Confirm your email address:", "This link expires in 24 hours.")

	if h.cfg.RequireVerifiedEmail {
		c.JSON(http.StatusCreated, gin.H{"message": "check your email to verify your account"})
		return
	}
	if err := h.startSession(c, u.ID); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, userJSON(u))
}

func (h *Handler) login(c *gin.Context) {
	var req credentials
	if !bind(c, &req) {
		return
	}
	invalid := func() { c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"}) }

	u, err := h.users.GetByEmail(c.Request.Context(), users.NormalizeEmail(req.Email))
	if errors.Is(err, users.ErrNotFound) {
		_, _ = VerifyPassword(req.Password, dummyHash) // equalise timing
		invalid()
		return
	}
	if err != nil {
		h.fail(c, err)
		return
	}
	ok, err := VerifyPassword(req.Password, u.PasswordHash)
	if err != nil {
		h.fail(c, err)
		return
	}
	if !ok {
		invalid()
		return
	}
	if h.cfg.RequireVerifiedEmail && u.EmailVerifiedAt == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "email_not_verified"})
		return
	}
	if err := h.startSession(c, u.ID); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, userJSON(u))
}

func (h *Handler) logout(c *gin.Context) {
	if raw, err := c.Cookie(CookieName); err == nil {
		_ = h.sessions.Delete(c.Request.Context(), raw)
	}
	h.setCookie(c, "", -1)
	c.Status(http.StatusNoContent)
}

func (h *Handler) me(c *gin.Context) {
	u, err := h.users.GetByID(c.Request.Context(), UserID(c))
	if errors.Is(err, users.ErrNotFound) {
		h.setCookie(c, "", -1)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, userJSON(u))
}

func (h *Handler) verifyEmail(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	if !bind(c, &req) {
		return
	}
	uid, err := h.tokens.Consume(c.Request.Context(), req.Token, PurposeVerifyEmail)
	if errors.Is(err, ErrInvalidToken) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this link is invalid or has expired"})
		return
	}
	if err != nil {
		h.fail(c, err)
		return
	}
	if err := h.users.MarkEmailVerified(c.Request.Context(), uid); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "email verified"})
}

func (h *Handler) resendVerification(c *gin.Context) {
	u, err := h.users.GetByID(c.Request.Context(), UserID(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	if u.EmailVerifiedAt == nil {
		h.sendTokenMail(u.ID, u.Email, PurposeVerifyEmail, verifyTTL, "/verify-email",
			"Confirm your Rovingr email", "Confirm your email address:", "This link expires in 24 hours.")
	}
	c.Status(http.StatusAccepted)
}

func (h *Handler) forgotPassword(c *gin.Context) {
	var req struct {
		Email string `json:"email"`
	}
	if !bind(c, &req) {
		return
	}
	if email, ok := validateEmail(req.Email); ok {
		if u, err := h.users.GetByEmail(c.Request.Context(), email); err == nil {
			h.sendTokenMail(u.ID, u.Email, PurposeResetPassword, resetTTL, "/reset-password",
				"Reset your Rovingr password", "Someone asked to reset your Rovingr password. Choose a new one here:",
				"This link expires in 1 hour. If this wasn't you, you can ignore this email.")
		}
	}
	// Same answer whether or not the account exists.
	c.JSON(http.StatusAccepted, gin.H{"message": "if an account exists, a reset link has been sent"})
}

func (h *Handler) resetPassword(c *gin.Context) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !bind(c, &req) {
		return
	}
	// Validate first so a weak password does not burn the one-time token.
	if err := ValidatePassword(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := c.Request.Context()
	uid, err := h.tokens.Consume(ctx, req.Token, PurposeResetPassword)
	if errors.Is(err, ErrInvalidToken) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this link is invalid or has expired"})
		return
	}
	if err != nil {
		h.fail(c, err)
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		h.fail(c, err)
		return
	}
	if err := h.users.UpdatePassword(ctx, uid, hash); err != nil {
		h.fail(c, err)
		return
	}
	_ = h.users.MarkEmailVerified(ctx, uid)   // they proved control of the inbox
	_ = h.sessions.DeleteAllForUser(ctx, uid) // sign out everywhere
	c.JSON(http.StatusOK, gin.H{"message": "password updated, please log in"})
}

// ---------- helpers ----------

func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<16)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

func validateEmail(raw string) (string, bool) {
	e := users.NormalizeEmail(raw)
	if len(e) < 3 || len(e) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e {
		return "", false
	}
	if !strings.Contains(e[strings.LastIndex(e, "@"):], ".") {
		return "", false
	}
	return e, true
}

func userJSON(u *users.User) gin.H {
	return gin.H{"id": u.ID, "email": u.Email, "emailVerified": u.EmailVerifiedAt != nil}
}

func (h *Handler) fail(c *gin.Context, err error) {
	log.Printf("auth error: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
}

// startSession always issues a brand-new session ID (prevents fixation) and
// discards any session the browser was already holding.
func (h *Handler) startSession(c *gin.Context, userID string) error {
	if old, err := c.Cookie(CookieName); err == nil {
		_ = h.sessions.Delete(c.Request.Context(), old)
	}
	raw, exp, err := h.sessions.Create(c.Request.Context(), userID)
	if err != nil {
		return err
	}
	h.setCookie(c, raw, int(time.Until(exp).Seconds()))
	return nil
}

func (h *Handler) setCookie(c *gin.Context, value string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	// Host-only cookie (no Domain), HttpOnly, Secure in production.
	c.SetCookie(CookieName, value, maxAge, "/", "", h.cfg.CookieSecure, true)
}

// sendTokenMail creates a one-time token and emails a link to the frontend.
// It runs in the background so response time never reveals whether an email was sent.
func (h *Handler) sendTokenMail(userID, email, purpose string, ttl time.Duration, path, subject, lead, tail string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		tok, err := h.tokens.Create(ctx, userID, purpose, ttl)
		if err != nil {
			log.Printf("create %s token: %v", purpose, err)
			return
		}
		// link := h.cfg.FrontendOrigin + path + "?token=" + url.QueryEscape(tok)
		link := "http://zanele.zelisline.com:8080/api/v1/auth" + path +
    "?token=" + url.QueryEscape(tok)
		text := lead + "\n\n" + link + "\n\n" + tail
		html := "<p>" + lead + "</p><p><a href=\"" + link + "\">" + link + "</a></p><p>" + tail + "</p>"
		if err := h.mailer.Send(ctx, email, subject, html, text); err != nil {
			log.Printf("send %s email: %v", purpose, err)
		}
	}()
}


func (h *Handler) verifyEmailPage(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.String(http.StatusBadRequest, "Missing verification token.")
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	// c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'none'; base-uri 'none'")
	c.Header("Content-Security-Policy",
    "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'none'; base-uri 'none'")

	page := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Verify your Rovingr email</title>
  <style>
    body {
      font-family: system-ui, sans-serif;
      max-width: 440px;
      margin: 15vh auto;
      padding: 24px;
      color: #172033;
      background: #f8fafc;
    }
    main {
      background: white;
      padding: 32px;
      border-radius: 16px;
      box-shadow: 0 8px 30px #0001;
    }
    button {
      padding: 12px 18px;
      border: 0;
      border-radius: 8px;
      background: #172033;
      color: white;
      font-size: 16px;
      cursor: pointer;
    }
    #status { margin-top: 20px; }
  </style>
</head>
<body>
  <main>
    <h1>Confirm your email</h1>
    <p>Click below to verify your Rovingr account.</p>
    <button id="confirm">Confirm email</button>
    <p id="status" role="status"></p>
  </main>
  <script>
    const token = new URLSearchParams(location.search).get("token");
    const button = document.getElementById("confirm");
    const status = document.getElementById("status");

    button.addEventListener("click", async () => {
      if (!token) {
        status.textContent = "The verification link is missing its token.";
        return;
      }

      button.disabled = true;
      status.textContent = "Verifying your email...";

      try {
        const response = await fetch("/api/v1/auth/verify-email", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token })
        });

        const result = await response.json();

        if (!response.ok) {
          status.textContent = result.error || "Verification failed. The link may have expired.";
          button.disabled = false;
          return;
        }

        status.textContent = "Your email has been verified. You can now log in.";
        button.remove();
        history.replaceState(null, "", location.pathname);
      } catch {
        status.textContent = "Could not contact the server. Please try again.";
        button.disabled = false;
      }
    });
  </script>
</body>
</html>`

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
}
