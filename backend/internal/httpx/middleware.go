package httpx

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"log"
)

// CORS allows exactly one origin, with credentials (cookies).
func CORS(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {

	log.Printf(
    "Origin check: received=%q expected=%q",
    c.GetHeader("Origin"),
    origin,
		)
		if c.GetHeader("Origin") == origin {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// RequireSameOrigin is the CSRF defence for cookie auth: state-changing
// requests must come from the configured frontend origin. Together with
// SameSite=Lax cookies and JSON-only bodies this blocks cross-site forgery.
// func RequireSameOrigin(origin string) gin.HandlerFunc {

	
// 	return func(c *gin.Context) {
// 		switch c.Request.Method {
// 		case http.MethodGet, http.MethodHead, http.MethodOptions:
// 			c.Next()
// 			return
// 		}
// 		if c.GetHeader("Origin") != origin {
// 			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
// 			return
// 		}
// 		c.Next()
// 	}
// }

func RequireSameOrigin(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Email verification uses a one-time token for authorization.
		// Allow only this specific POST endpoint to bypass the origin check.
		if c.Request.Method == http.MethodPost &&
			c.Request.URL.Path == "/api/v1/auth/verify-email" {
			c.Next()
			return
		}

		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		if c.GetHeader("Origin") != origin {
			c.AbortWithStatusJSON(
				http.StatusForbidden,
				gin.H{"error": "forbidden"},
			)
			return
		}

		c.Next()
	}
}


func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	}
}

type entry struct {
	l    *rate.Limiter
	seen time.Time
}

// RateLimit is a per-IP limiter held in memory. Fine for one API instance;
// move to Redis if you run several.
func RateLimit(every time.Duration, burst int) gin.HandlerFunc {
	var mu sync.Mutex
	m := map[string]*entry{}

	go func() {
		for range time.Tick(5 * time.Minute) {
			mu.Lock()
			for k, e := range m {
				if time.Since(e.seen) > 15*time.Minute {
					delete(m, k)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		mu.Lock()
		e, ok := m[ip]
		if !ok {
			e = &entry{l: rate.NewLimiter(rate.Every(every), burst)}
			m[ip] = e
		}
		e.seen = time.Now()
		allowed := e.l.Allow()
		mu.Unlock()

		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, try again later"})
			return
		}
		c.Next()
	}
}