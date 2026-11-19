package realtime

import (
	"github.com/gin-gonic/gin"
)

// Routes mounts GET /realtime/ws on the given group (the /api/v1 group).
//
// This is the only file in the package that knows about gin or your auth
// package; the rest is plain net/http so it can be tested on its own.
//
//	requireAuth   authHandler.RequireAuth()
//	userID        auth.UserID
//	cookieName    auth.CookieName
func Routes(rg *gin.RouterGroup, hub *Hub, requireAuth gin.HandlerFunc,
	userID func(*gin.Context) string, cookieName, frontendOrigin string) {

	rg.GET("/realtime/ws", requireAuth, func(c *gin.Context) {
		token, _ := c.Cookie(cookieName)
		hub.Serve(c.Writer, c.Request, userID(c), token, frontendOrigin)
	})
}
