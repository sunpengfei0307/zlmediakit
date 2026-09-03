package server

import (
	"testing"
	"time"
	"zlm-admin/service"
)

func TestDelCancelsLifecycleContext(t *testing.T) {
	oldHub := service.H
	service.H = nil
	defer func() { service.H = oldHub }()

	s := New()
	if err := s.Del(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.ctxt.Done():
	case <-time.After(time.Second):
		t.Fatal("server context was not cancelled")
	}
}
