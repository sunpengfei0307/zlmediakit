package service

import (
	"testing"
	"zlm-admin/core/config"
)

func TestKickEmptyID(t *testing.T) {
	h := &Hub{}
	got := h.kickSession(config.Node{}, "  ")
	if got["code"] != -1 {
		t.Fatalf("%v", got)
	}
	msg, _ := got["msg"].(string)
	if msg == "" || msg == "已踢掉" {
		t.Fatalf("msg=%q", msg)
	}
}
