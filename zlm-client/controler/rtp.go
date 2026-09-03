package controler

import (
	"net/http"
	"net/url"
	"strings"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func (Page) RTP(c *gin.Context) {
	Page{}.renderProtocols(c, protocolTabOf(c, "rtp"), "")
}

func (Page) RTPOpen(c *gin.Context)          { Page{}.rtpMutation(c, service.RTPOpenServer) }
func (Page) RTPOpenMultiplex(c *gin.Context) { Page{}.rtpMutation(c, service.RTPOpenServerMultiplex) }
func (Page) RTPConnect(c *gin.Context)       { Page{}.rtpMutation(c, service.RTPConnectServer) }
func (Page) RTPClose(c *gin.Context)         { Page{}.rtpMutation(c, service.RTPCloseServer) }
func (Page) RTPUpdateSSRC(c *gin.Context)    { Page{}.rtpMutation(c, service.RTPUpdateSSRC) }
func (Page) RTPPauseCheck(c *gin.Context)    { Page{}.rtpMutation(c, service.RTPPauseCheck) }
func (Page) RTPResumeCheck(c *gin.Context)   { Page{}.rtpMutation(c, service.RTPResumeCheck) }
func (Page) RTPStartSend(c *gin.Context)     { Page{}.rtpMutation(c, service.RTPStartSend) }
func (Page) RTPStartSendPassive(c *gin.Context) {
	Page{}.rtpMutation(c, service.RTPStartSendPassive)
}
func (Page) RTPStartSendTalk(c *gin.Context) { Page{}.rtpMutation(c, service.RTPStartSendTalk) }
func (Page) RTPStopSend(c *gin.Context)      { Page{}.rtpMutation(c, service.RTPStopSend) }

var rtpFormKeys = []string{
	"vhost", "app", "stream", "stream_id", "port", "tcp_mode", "local_ip", "only_track",
	"re_use_port", "ssrc", "dst_url", "dst_port", "pause_seconds", "is_udp", "src_port",
	"pt", "type", "only_audio", "from_mp4", "ssrc_multi_send", "udp_rtcp_timeout",
	"close_delay_ms", "recv_stream_id", "enable_origin_recv_limit",
}

func (Page) rtpMutation(c *gin.Context, action string) {
	_ = c.Request.ParseForm()
	q := url.Values{}
	for _, key := range rtpFormKeys {
		if value, exists := c.GetPostForm(key); exists {
			q.Set(key, value)
		}
	}
	if service.H == nil {
		Page{}.renderProtocols(c, "rtp", "RTP 服务不可用")
		return
	}
	result := service.H.RTPOperation(firstNodeID(), loginUserOf(c), action, q)
	msg := operationPageMessage(result, rtpActionLabel(action)+"成功", rtpActionLabel(action)+"失败")
	setToast(c, msg)
	c.Set("rtp_query_vhost", strings.TrimSpace(c.PostForm("view_vhost")))
	c.Set("rtp_query_app", strings.TrimSpace(c.PostForm("view_app")))
	c.Set("rtp_query_stream", strings.TrimSpace(c.PostForm("view_stream")))
	c.Request.Method = http.MethodGet
	Page{}.renderProtocols(c, "rtp", msg)
}

func rtpActionLabel(action string) string {
	return map[string]string{
		service.RTPOpenServer:          "创建 RTP 接收服务",
		service.RTPOpenServerMultiplex: "创建 RTP 多路复用接收服务",
		service.RTPConnectServer:       "连接 RTP 服务端",
		service.RTPCloseServer:         "关闭 RTP 接收服务",
		service.RTPUpdateSSRC:          "更新 RTP SSRC",
		service.RTPPauseCheck:          "暂停 RTP 超时检查",
		service.RTPResumeCheck:         "恢复 RTP 超时检查",
		service.RTPStartSend:           "启动 RTP 主动发送",
		service.RTPStartSendPassive:    "启动 RTP 被动 TCP 发送",
		service.RTPStartSendTalk:       "启动 RTP 对讲",
		service.RTPStopSend:            "停止 RTP 发送",
	}[action]
}
