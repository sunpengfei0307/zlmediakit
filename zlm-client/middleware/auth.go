package middleware

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func RequireLogin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if service.AuthSkip(c.Request.URL.Path) {
			c.Next()
			return
		}
		token, _ := c.Cookie(service.SessCookie)
		user, ok := service.ParseSession(token, time.Now())
		if !ok {
			next := c.Request.URL.RequestURI()
			if c.GetHeader("HX-Request") == "true" {
				c.Header("HX-Redirect", "/login?next="+url.QueryEscape(next))
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			if strings.HasPrefix(c.Request.URL.Path, "/api") {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": -1, "msg": "未登录"})
				return
			}
			c.Redirect(http.StatusFound, "/login?next="+url.QueryEscape(next))
			c.Abort()
			return
		}
		c.Set("login_user", user)
		c.Next()
	}
}
