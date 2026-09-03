package controler

import (
	"net/url"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func advancedAction(raw string) (string, bool) {
	switch raw {
	case "restart":
		return service.AdvancedRestart, true
	case "delete-record-dir":
		return service.AdvancedDeleteRecordDir, true
	case "delete-snap-dir":
		return service.AdvancedDeleteSnapDir, true
	case "broadcast":
		return service.AdvancedBroadcast, true
	default:
		return "", false
	}
}

func (Page) ConfigAdvanced(c *gin.Context) {
	action, ok := advancedAction(c.Param("op"))
	if !ok {
		Page{}.renderConfig(c, "不支持的高级操作", nil, nil, nil)
		return
	}
	_ = c.Request.ParseForm()
	q := url.Values{}
	for _, key := range []string{
		"schema", "vhost", "app", "stream", "period", "name", "file", "template", "msg",
	} {
		if value, exists := c.GetPostForm(key); exists {
			q.Set(key, value)
		}
	}
	if service.H == nil {
		Page{}.renderConfig(c, "高级操作服务不可用", nil, nil, nil)
		return
	}
	result := service.H.AdvancedOperation(firstNodeID(), loginUserOf(c), action, q)
	notice := operationPageMessage(result, advancedActionLabel(action), advancedActionLabel(action)+"失败")
	setToast(c, notice)
	Page{}.renderConfig(c, notice, nil, nil, nil)
}

func advancedActionLabel(action string) string {
	switch action {
	case service.AdvancedRestart:
		return "已请求重启 MediaServer"
	case service.AdvancedDeleteRecordDir:
		return "已删除受限录像目录"
	case service.AdvancedDeleteSnapDir:
		return "已删除受限截图目录"
	case service.AdvancedBroadcast:
		return "已发送模板广播"
	default:
		return "高级操作完成"
	}
}
