package controler

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func (Page) Sources(c *gin.Context) {
	Page{}.renderSources(c, "", nil)
}

func (Page) SourcePullAdd(c *gin.Context) {
	Page{}.sourceTaskMutation(c, service.SourceTaskPullAdd)
}

func (Page) SourcePullDelete(c *gin.Context) {
	Page{}.sourceTaskMutation(c, service.SourceTaskPullDelete)
}

func (Page) SourcePusherAdd(c *gin.Context) {
	Page{}.sourceTaskMutation(c, service.SourceTaskPusherAdd)
}

func (Page) SourcePusherDelete(c *gin.Context) {
	Page{}.sourceTaskMutation(c, service.SourceTaskPusherDelete)
}

func (Page) SourceFFmpegAdd(c *gin.Context) {
	Page{}.sourceTaskMutation(c, service.SourceTaskFFmpegAdd)
}

func (Page) SourceFFmpegDelete(c *gin.Context) {
	Page{}.sourceTaskMutation(c, service.SourceTaskFFmpegDelete)
}

func sourceTaskKeepForm(action string) bool {
	switch action {
	case service.SourceTaskPullAdd, service.SourceTaskPusherAdd, service.SourceTaskFFmpegAdd:
		return true
	default:
		return false
	}
}

func (Page) sourceTaskMutation(c *gin.Context, action string) {
	_ = c.Request.ParseForm()
	var form url.Values
	if sourceTaskKeepForm(action) {
		form = c.Request.PostForm
	}
	if service.H == nil {
		Page{}.renderSources(c, "信号转发服务不可用", form)
		return
	}
	result := service.H.SourceTaskOperation(
		firstNodeID(), loginUserOf(c), action, c.Request.PostForm, isLocalAdmin(c),
	)
	label := sourceTaskActionLabel(action)
	msg := fmt.Sprintf("%s成功", label)
	if asI(result["code"]) != 0 {
		msg = fmt.Sprintf("%s失败：%s", label, asStr(result["msg"]))
	} else {
		form = nil
		if detail := asStr(result["msg"]); detail != "" && detail != "操作成功" {
			msg += "：" + detail
		}
	}
	setToast(c, msg)
	Page{}.renderSources(c, msg, form)
}

func (Page) renderSources(c *gin.Context, notice string, form url.Values) {
	tasks := service.SourceTasksView{}
	if service.H != nil {
		tasks = service.H.ListSourceTasks(firstNodeID())
	}
	tab := strings.TrimSpace(c.Query("tab"))
	if tab == "" {
		tab = strings.TrimSpace(c.PostForm("view_tab"))
	}
	switch tab {
	case "pull", "pusher", "ffmpeg":
	default:
		switch {
		case strings.Contains(notice, "拉流"):
			tab = "pull"
		case strings.Contains(notice, "推流"):
			tab = "pusher"
		case strings.Contains(notice, "FFmpeg"):
			tab = "ffmpeg"
		default:
			tab = "pull"
		}
	}
	Page{}.render(c, "sources", gin.H{"Tasks": tasks, "Notice": notice, "Tab": tab, "Form": form})
}

func sourceTaskActionLabel(action string) string {
	switch action {
	case service.SourceTaskPullAdd:
		return "拉流代理新增"
	case service.SourceTaskPullDelete:
		return "拉流代理删除"
	case service.SourceTaskPusherAdd:
		return "推流代理新增"
	case service.SourceTaskPusherDelete:
		return "推流代理删除"
	case service.SourceTaskFFmpegAdd:
		return "FFmpeg 源新增"
	case service.SourceTaskFFmpegDelete:
		return "FFmpeg 源删除"
	default:
		return "信号转发操作"
	}
}

func isLocalAdmin(c *gin.Context) bool {
	if loginUserOf(c) != service.AdminUser() {
		return false
	}
	ip := net.ParseIP(c.ClientIP())
	return ip != nil && ip.IsLoopback()
}
