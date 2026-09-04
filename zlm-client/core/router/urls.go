package router

import (
	"io/fs"
	"net/http"
	"strings"
	"zlm-admin/controler"
	"zlm-admin/core/logger"

	"github.com/gin-gonic/gin"
)

func (r *Router) SetupLog() *Router {
	logger.Infor("@setup nfs router!")
	e := r.engine
	e.StaticFS("/log", http.Dir("log"))
	return r
}

func (r *Router) SetupWeb() *Router {
	logger.Infor("@setup web router!")
	return r
}

func methodNotAllowed(c *gin.Context) {
	c.JSON(http.StatusMethodNotAllowed, gin.H{"code": -1, "msg": "method not allowed"})
}

func postOnly(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			methodNotAllowed(c)
			return
		}
		handler(c)
	}
}

func (r *Router) SetupApp() *Router {
	logger.Infor("@setup app router!")
	e := r.engine
	api := controler.API{}
	page := controler.Page{}

	if WebFS != nil {
		if err := controler.LoadHTML(e, WebFS); err != nil {
			logger.Error("load templates: %v", err)
		}
		if webRoot, err := fs.Sub(WebFS, "web"); err == nil {
			if staticFS, err := fs.Sub(webRoot, "static"); err == nil {
				e.StaticFS("/static", http.FS(staticFS))
			}
			hfs := http.FS(webRoot)
			e.GET("/hls.min.js", func(c *gin.Context) { c.FileFromFS("hls.min.js", hfs) })
			e.GET("/mpegts.min.js", func(c *gin.Context) { c.FileFromFS("mpegts.min.js", hfs) })
		}
	}

	e.GET("/login", page.LoginForm)
	e.POST("/login", page.LoginPost)
	e.GET("/logout", page.Logout)
	e.GET("/", page.Overview)
	e.GET("/ui/overview", page.Overview)
	e.GET("/ui/stream-conns", page.StreamConns)
	e.GET("/streams", page.Streams)
	e.GET("/sessions", page.Sessions)
	e.Any("/ui/streams/close", postOnly(page.CloseStreams))
	e.Any("/ui/sessions/kick", postOnly(page.KickSessions))
	e.Any("/ui/sessions/kick-selected", postOnly(page.KickSelected))
	e.GET("/sources", page.Sources)
	e.Any("/sources/pull/add", postOnly(page.SourcePullAdd))
	e.Any("/sources/pull/delete", postOnly(page.SourcePullDelete))
	e.Any("/sources/pusher/add", postOnly(page.SourcePusherAdd))
	e.Any("/sources/pusher/delete", postOnly(page.SourcePusherDelete))
	e.Any("/sources/ffmpeg/add", postOnly(page.SourceFFmpegAdd))
	e.Any("/sources/ffmpeg/delete", postOnly(page.SourceFFmpegDelete))
	e.GET("/rtp", page.RTP)
	e.GET("/protocols", page.Protocols)
	e.Any("/rtp/open", postOnly(page.RTPOpen))
	e.Any("/rtp/open-multiplex", postOnly(page.RTPOpenMultiplex))
	e.Any("/rtp/connect", postOnly(page.RTPConnect))
	e.Any("/rtp/close", postOnly(page.RTPClose))
	e.Any("/rtp/update-ssrc", postOnly(page.RTPUpdateSSRC))
	e.Any("/rtp/pause-check", postOnly(page.RTPPauseCheck))
	e.Any("/rtp/resume-check", postOnly(page.RTPResumeCheck))
	e.Any("/rtp/start-send", postOnly(page.RTPStartSend))
	e.Any("/rtp/start-send-passive", postOnly(page.RTPStartSendPassive))
	e.Any("/rtp/start-send-talk", postOnly(page.RTPStartSendTalk))
	e.Any("/rtp/stop-send", postOnly(page.RTPStopSend))
	e.GET("/onvif-webrtc", page.OnvifWebRTC)
	e.Any("/onvif-webrtc/scan", postOnly(page.OnvifScan))
	e.Any("/onvif-webrtc/keeper/add", postOnly(page.WebRTCRoomKeeperAdd))
	e.Any("/onvif-webrtc/keeper/delete", postOnly(page.WebRTCRoomKeeperDelete))
	e.Any("/onvif-webrtc/import-pull", postOnly(page.OnvifImportPull))
	e.GET("/files", page.Files)
	e.Any("/files/record/:op", postOnly(page.FilesRecord))
	e.Any("/files/vod/:op", postOnly(page.FilesVOD))
	e.GET("/config", page.Config)
	e.POST("/ui/config/save", page.ConfigSave)
	e.POST("/ui/config/ops", page.ConfigOpsSave)
	e.Any("/config/advanced/:op", postOnly(page.ConfigAdvanced))
	e.GET("/push", page.Push)
	e.GET("/auth", page.Auth)
	e.Any("/auth/enable", postOnly(page.AuthEnable))
	e.Any("/auth/add", postOnly(page.AuthAdd))
	e.Any("/auth/delete", postOnly(page.AuthDelete))
	e.Any("/auth/toggle", postOnly(page.AuthToggle))
	e.Any("/auth/ip/mode", postOnly(page.AuthIPMode))
	e.Any("/auth/ip/add", postOnly(page.AuthIPAdd))
	e.Any("/auth/ip/toggle", postOnly(page.AuthIPToggle))
	e.Any("/auth/ip/delete", postOnly(page.AuthIPDelete))
	e.GET("/events", page.Events)
	e.GET("/logs", page.Logs)
	e.Any("/ui/kick", postOnly(page.Kick))
	e.GET("/index.html", func(c *gin.Context) { c.Redirect(http.StatusFound, "/") })
	e.GET("/admin.crt", serveAdminCert)

	e.GET("/api/overview", api.Overview)
	e.GET("/api/nodes", api.Nodes)
	e.GET("/api/events", api.Events)
	e.GET("/api/local/metrics", api.LocalMetrics)
	e.GET("/api/logs/stream", api.LogStream)
	e.GET("/api/logs", api.Logs)
	e.GET("/api/history", api.History)
	e.GET("/api/node/:id/media/*filepath", api.NodeMedia)
	e.GET("/api/node/:id/fmp4-list", api.FMP4List)
	e.GET("/api/node/:id/snap", api.NodeSnap)
	e.POST("/api/node/:id/snap", api.NodeSnap)
	e.Any("/api/node/:id/zlm/*filepath", api.NodeZLM)
	e.Any("/api/node/:id", api.Node)
	e.Any("/api/node/:id/:action", api.Node)
	e.Any("/hook/:event", api.Hook)
	e.Any("/index/hook/:event", api.Hook)

	e.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") || strings.HasPrefix(c.Request.URL.Path, "/hook") {
			c.JSON(http.StatusNotFound, gin.H{"code": -1, "msg": "not found"})
			return
		}
		c.Redirect(http.StatusFound, "/")
	})
	return r
}
