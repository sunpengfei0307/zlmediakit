package controler

import (
	"strings"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func (Page) Protocols(c *gin.Context) {
	Page{}.renderProtocols(c, protocolTabOf(c, ""), "")
}

func protocolTabOf(c *gin.Context, fallback string) string {
	tab := strings.TrimSpace(c.Query("tab"))
	if tab == "" {
		tab = strings.TrimSpace(c.PostForm("view_tab"))
	}
	if tab == "" {
		tab = fallback
	}
	switch tab {
	case "rtp", "onvif", "webrtc":
		return tab
	}
	return ""
}

func pickProtocolTab(tab string, layout service.ProtocolLayout) string {
	switch tab {
	case "rtp", "onvif", "webrtc":
		return tab
	}
	if layout.ONVIF {
		return "onvif"
	}
	if layout.WebRTC {
		return "webrtc"
	}
	if layout.RTP {
		return "rtp"
	}
	return "onvif"
}

func (Page) renderProtocols(c *gin.Context, tab, notice string) {
	layout := service.ProtocolLayout{RTP: true, ONVIF: true, WebRTC: true}
	if service.H != nil {
		layout = service.H.ProtocolLayout(firstNodeID())
	}
	tab = pickProtocolTab(tab, layout)

	vhost, app, stream := c.Query("vhost"), c.Query("app"), c.Query("stream")
	if value, ok := c.Get("rtp_query_vhost"); ok && strings.TrimSpace(asStr(value)) != "" {
		vhost = strings.TrimSpace(asStr(value))
	}
	if value, ok := c.Get("rtp_query_app"); ok && strings.TrimSpace(asStr(value)) != "" {
		app = strings.TrimSpace(asStr(value))
	}
	if value, ok := c.Get("rtp_query_stream"); ok && strings.TrimSpace(asStr(value)) != "" {
		stream = strings.TrimSpace(asStr(value))
	}
	if vhost == "" {
		vhost = "__defaultVhost__"
	}

	rtpView := service.RTPView{}
	onvifView := service.OnvifWebRTCView{}
	devices := []map[string]any{}
	playerKey := strings.TrimSpace(c.Query("key"))
	if service.H != nil {
		if layout.RTP && tab == "rtp" {
			rtpView = service.H.ListRTP(firstNodeID(), vhost, app, stream)
		}
		if (layout.ONVIF || layout.WebRTC) && (tab == "onvif" || tab == "webrtc") {
			onvifView = service.H.ListOnvifWebRTC(firstNodeID(), playerKey)
		}
	}
	if cached, ok := c.Get("onvif_devices"); ok {
		if rows, ok := cached.([]map[string]any); ok && rows != nil {
			devices = rows
		}
	}

	Page{}.render(c, "protocols", gin.H{
		"Tab": tab, "Notice": notice,
		"EnableRTP": layout.RTP, "EnableONVIF": layout.ONVIF, "EnableWebRTC": layout.WebRTC,
		"RTPReady": service.ProtocolReady(layout.RTP, rtpView.Receivers.Error, rtpView.Senders.Error),
		"ONVIFReady": service.ProtocolReady(layout.ONVIF),
		"WebRTCReady": service.ProtocolReady(layout.WebRTC, onvifView.Rooms.Error, onvifView.Keepers.Error),
		"RTP": rtpView, "RTC": onvifView, "Devices": devices, "PlayerKey": playerKey,
		"QueryVhost": vhost, "QueryApp": app, "QueryStream": stream,
	})
}
