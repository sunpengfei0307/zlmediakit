package controler

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

// API 服务入口，负责处理路由、参数校验、请求转发.
type API struct{}

func isCoreMutation(action string) bool {
	switch action {
	case "close_stream", "close_streams", "kick", "kick_session", "kick_sessions":
		return true
	default:
		return false
	}
}

func (API) Overview(c *gin.Context) {
	c.JSON(http.StatusOK, service.H.Overview())
}

func (API) Nodes(c *gin.Context) {
	c.JSON(http.StatusOK, service.H.Nodes())
}

func (API) NodeMedia(c *gin.Context) {
	service.H.ServeMedia(c.Writer, c.Request, c.Param("id"), c.Param("filepath"))
}

func (API) NodeZLM(c *gin.Context) {
	rel := strings.TrimPrefix(c.Param("filepath"), "/")
	service.H.ProxyZLM(c.Writer, c.Request, c.Param("id"), rel)
}

func (API) FMP4List(c *gin.Context) {
	service.H.ServeFMP4List(c.Writer, c.Request, c.Param("id"), c.Query("path"))
}

func (API) NodeSnap(c *gin.Context) {
	service.H.ServeSnap(c.Writer, c.Request, c.Param("id"), c.Query("app"), c.Query("stream"))
}

func (API) Node(c *gin.Context) {
	id := c.Param("id")
	action := c.Param("action")
	if isCoreMutation(action) {
		if c.Request.Method != http.MethodPost {
			c.JSON(http.StatusMethodNotAllowed, gin.H{"code": -1, "msg": "method not allowed"})
			return
		}
		_ = c.Request.ParseForm()
		c.JSON(http.StatusOK, service.H.CoreOperation(id, loginUserOf(c), action, c.Request.Form))
		return
	}
	if action == "file" {
		service.H.ServeFile(c.Writer, c.Request, id, c.Query("path"))
		return
	}
	host := c.Query("host")
	if host == "" {
		host = strings.Split(c.Request.Host, ":")[0]
	}
	body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	data, status, raw := service.H.NodeAction(id, action, host, c.Request.URL.Query(), body)
	if raw != nil {
		c.Data(status, "application/json; charset=utf-8", raw)
		return
	}
	c.JSON(status, data)
}

func (API) Events(c *gin.Context) {
	c.JSON(http.StatusOK, service.H.Events())
}

func (API) Logs(c *gin.Context) {
	n := 1200
	if v := c.Query("n"); v != "" {
		if x, err := strconv.Atoi(v); err == nil {
			n = x
		}
	}
	if n < 100 {
		n = 100
	}
	if n > 3000 {
		n = 3000
	}
	c.JSON(http.StatusOK, service.H.Logs(c.Query("node"), c.Query("file"), c.Query("source"), c.Query("lv"), n))
}

func (API) LogStream(c *gin.Context) {
	offset, _ := strconv.ParseInt(c.Query("offset"), 10, 64)
	if err := service.H.LogStream(c.Request.Context(), c.Writer, c.Query("node"), c.Query("file"), c.Query("source"), c.Query("lv"), offset); err != nil {
		if !c.Writer.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"code": -1, "msg": err.Error()})
		}
	}
}

func (API) LocalMetrics(c *gin.Context) {
	c.JSON(http.StatusOK, service.H.LocalMetrics())
}

func (API) History(c *gin.Context) {
	c.JSON(http.StatusOK, service.H.History(c.Query("range")))
}

func (API) Hook(c *gin.Context) {
	event := c.Param("event")
	raw, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	c.JSON(http.StatusOK, service.H.Hook(event, raw))
}
