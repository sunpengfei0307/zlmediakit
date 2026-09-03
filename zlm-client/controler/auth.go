package controler

import (
	"net/http"
	"strings"
	"time"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func loginUserOf(c *gin.Context) string {
	if v, ok := c.Get("login_user"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	tok, _ := c.Cookie(service.SessCookie)
	user, ok := service.ParseSession(tok, time.Now())
	if ok {
		return user
	}
	return ""
}

func safeNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "://") {
		return "/"
	}
	return next
}

func setSessionCookie(c *gin.Context, token string, exp time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     service.SessCookie,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(time.Until(exp).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Request.TLS != nil,
	})
}

func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     service.SessCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (Page) LoginForm(c *gin.Context) {
	if loginUserOf(c) != "" {
		c.Redirect(http.StatusFound, safeNext(c.Query("next")))
		return
	}
	c.HTML(http.StatusOK, "login", gin.H{
		"Err": "", "Next": c.Query("next"), "User": service.AdminUser(),
	})
}

func (Page) LoginPost(c *gin.Context) {
	user := c.PostForm("user")
	pass := c.PostForm("pass")
	next := c.PostForm("next")
	if err := service.CheckLogin(user, pass); err != nil {
		c.HTML(http.StatusOK, "login", gin.H{
			"Err": err.Error(), "Next": next, "User": user,
		})
		return
	}
	tok, exp := service.IssueSession(service.AdminUser(), time.Now())
	setSessionCookie(c, tok, exp)
	c.Redirect(http.StatusFound, safeNext(next))
}

func (Page) Logout(c *gin.Context) {
	clearSessionCookie(c)
	c.Redirect(http.StatusFound, "/login")
}
