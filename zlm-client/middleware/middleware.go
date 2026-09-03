package middleware

import (
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime/debug"
	"strings"
	"time"
	"zlm-admin/core/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func quietAccess(path string) bool {
	switch path {
	case "/", "/index.html", "/favicon.ico", "/admin.crt", "/ui/overview",
		"/login", "/logout", "/api/logs/stream":
		return true
	}
	if strings.HasPrefix(path, "/hook/") || strings.HasPrefix(path, "/index/hook/") {
		return true
	}
	if strings.HasPrefix(path, "/static/") || strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") || strings.HasSuffix(path, ".ico") || strings.HasSuffix(path, ".map") {
		return true
	}
	return false
}

func GinZlogger() gin.HandlerFunc {
	zlog := logger.Loggers["web"].Zlog.WithOptions(zap.WithCaller(false))
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		status := c.Writer.Status()
		cost := time.Since(start)
		if quietAccess(path) && status < 400 && cost < 500*time.Millisecond {
			return
		}
		fields := []zap.Field{
			zap.Int("status", status),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("ip", c.ClientIP()),
			zap.Duration("cost", cost),
		}
		if q := c.Request.URL.RawQuery; q != "" {
			fields = append(fields, zap.String("query", q))
		}
		if err := c.Errors.ByType(gin.ErrorTypePrivate).String(); err != "" {
			fields = append(fields, zap.String("error", err))
		}
		if status >= 500 {
			zlog.Error(path, fields...)
			return
		}
		if status >= 400 {
			zlog.Warn(path, fields...)
			return
		}
		zlog.Info(path, fields...)
	}
}

func GinRecover() gin.HandlerFunc {
	zlog := logger.Loggers["web"].Zlog
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				var brokenPipe bool
				if ne, ok := err.(*net.OpError); ok {
					if se, ok := ne.Err.(*os.SyscallError); ok {
						msg := strings.ToLower(se.Error())
						if strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer") {
							brokenPipe = true
						}
					}
				}
				httpRequest, _ := httputil.DumpRequest(c.Request, false)
				if brokenPipe {
					zlog.Error(c.Request.URL.Path,
						zap.Any("error", err),
						zap.String("request", string(httpRequest)),
					)
					c.Abort()
					return
				}
				zlog.Error("[Recovery from panic]",
					zap.Any("error", err),
					zap.String("request", string(httpRequest)),
					zap.String("stack", string(debug.Stack())),
				)
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

func AddPlugins(e *gin.Engine) {
	e.Use(GinZlogger(), GinRecover())
	e.Use(GinCors())
	e.Use(RequireLogin())
}
