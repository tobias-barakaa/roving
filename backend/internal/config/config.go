package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	Env                  string // "development" or "production"
	FrontendOrigin       string // exact origin of the Astro site, no trailing slash
	CookieSecure         bool
	ResendAPIKey         string // empty => emails are printed to the console
	MailFrom             string
	RequireVerifiedEmail bool
	SessionTTL           time.Duration // absolute lifetime
	SessionIdleTTL       time.Duration // max gap between requests
}

func Load() (*Config, error) {
	c := &Config{
		Env:                  get("APP_ENV", "development"),
		FrontendOrigin:       strings.TrimRight(get("FRONTEND_ORIGIN", "http://localhost:4321"), "/"),
		ResendAPIKey:         strings.TrimSpace(os.Getenv("RESEND_API_KEY")),
		MailFrom:             get("MAIL_FROM", "Rovingr <onboarding@resend.dev>"),
		RequireVerifiedEmail: get("REQUIRE_VERIFIED_EMAIL", "false") == "true",
		SessionTTL:           30 * 24 * time.Hour,
		SessionIdleTTL:       7 * 24 * time.Hour,
	}
	c.CookieSecure = c.Env == "production"

	if c.Env == "production" && !strings.HasPrefix(c.FrontendOrigin, "https://") {
		return nil, fmt.Errorf("FRONTEND_ORIGIN must be https in production")
	}
	return c, nil
}

func get(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}