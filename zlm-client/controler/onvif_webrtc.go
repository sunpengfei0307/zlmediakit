package controler

import (
	"net/http"
	"net/url"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func (Page) OnvifWebRTC(c *gin.Context) {
	tab := protocolTabOf(c, "onvif")
	if tab == "rtp" {
		tab = "onvif"
	}
	Page{}.renderProtocols(c, tab, "")
}

func (Page) OnvifScan(c *gin.Context) {
	_ = c.Request.ParseForm()
	if service.H == nil {
		Page{}.renderProtocols(c, "onvif", "ONVIF 发现服务不可用")
		return
	}
	result := service.H.SearchOnvifDevices(firstNodeID(), loginUserOf(c), url.Values{
		"timeout_ms":    {c.PostForm("timeout_ms")},
		"subnet_prefix": {c.PostForm("subnet_prefix")},
	})
	notice := operationPageMessage(result, "ONVIF 发现完成", "ONVIF 发现失败")
	setToast(c, notice)
	devices, _ := result["devices"].([]map[string]any)
	c.Set("onvif_devices", devices)
	c.Request.Method = http.MethodGet
	Page{}.renderProtocols(c, "onvif", notice)
}

func (Page) WebRTCRoomKeeperAdd(c *gin.Context) {
	Page{}.webRTCRoomKeeperMutation(c, service.WebRTCRoomKeeperAdd)
}

func (Page) WebRTCRoomKeeperDelete(c *gin.Context) {
	Page{}.webRTCRoomKeeperMutation(c, service.WebRTCRoomKeeperDelete)
}

func (Page) webRTCRoomKeeperMutation(c *gin.Context, action string) {
	_ = c.Request.ParseForm()
	if service.H == nil {
		Page{}.renderProtocols(c, "webrtc", "WebRTC RoomKeeper 服务不可用")
		return
	}
	q := url.Values{}
	for _, key := range []string{"server_host", "server_port", "room_id", "ssl", "room_key"} {
		if value, exists := c.GetPostForm(key); exists {
			q.Set(key, value)
		}
	}
	result := service.H.WebRTCRoomKeeperOperation(firstNodeID(), loginUserOf(c), action, q)
	success := "RoomKeeper 已添加"
	if action == service.WebRTCRoomKeeperDelete {
		success = "RoomKeeper 已删除"
	}
	notice := operationPageMessage(result, success, "RoomKeeper 操作失败")
	setToast(c, notice)
	c.Request.Method = http.MethodGet
	Page{}.renderProtocols(c, "webrtc", notice)
}

func (Page) OnvifImportPull(c *gin.Context) {
	_ = c.Request.ParseForm()
	if service.H == nil {
		Page{}.renderProtocols(c, "onvif", "拉流代理服务不可用")
		return
	}
	q := url.Values{
		"url":    {c.PostForm("url")},
		"vhost":  {c.PostForm("vhost")},
		"app":    {c.PostForm("app")},
		"stream": {c.PostForm("stream")},
	}
	result := service.H.ImportOnvifPull(firstNodeID(), loginUserOf(c), q, isLocalAdmin(c))
	notice := operationPageMessage(result, "拉流代理已导入", "拉流代理导入失败")
	setToast(c, notice)
	c.Request.Method = http.MethodGet
	Page{}.renderProtocols(c, "onvif", notice)
}
