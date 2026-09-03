package server

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
	"zlm-admin/core/router"
	"zlm-admin/service"
	"zlm-admin/utils/extern"
	"zlm-admin/utils/helper"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type Server struct {
	Type   string
	Role   string
	Port   string
	Mode   string
	mods   map[string]func() error
	webs   map[string]*router.Router
	stat   extern.Status
	egrp   *errgroup.Group
	ctxt   context.Context
	cancel context.CancelFunc
}

var once sync.Once
var srvs []*Server

func New(args ...any) *Server {
	port := fmt.Sprintf("%v", config.C.Basic.Port)
	if len(args) == 1 {
		port = args[0].(string)
	}
	s := &Server{
		Type: config.C.Basic.Type,
		Role: config.C.Basic.Role,
		Port: port,
		Mode: config.C.Basic.Mode,
		stat: extern.S_INITIAL,
		mods: make(map[string]func() error),
		webs: make(map[string]*router.Router),
	}
	base, cancel := context.WithCancel(context.Background())
	s.egrp, s.ctxt = errgroup.WithContext(base)
	s.cancel = cancel
	return s
}

func (s *Server) sig() error {
	srvs = append(srvs, s)
	once.Do(func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
		for v := range sigCh {
			for _, srv := range srvs {
				_ = srv.Del()
			}
			close(sigCh)
			logger.Warnf("@recv sig='%v', clean srvs=%v, exit!", v, srvs)
		}
		os.Exit(0)
	})
	return nil
}

func (s *Server) Set() *Server {
	s.stat.Set(extern.S_SETTING)
	defer s.stat.Set(extern.S_SETUPED)
	service.Init()
	s.mods["sig"] = func() error { return s.sig() }
	s.mods["sample"] = func() error {
		service.H.SampleLoop()
		return nil
	}
	s.mods["snap"] = func() error {
		service.H.SnapLoop(s.ctxt)
		return nil
	}
	s.mods[s.Type] = func() error {
		s.webs[s.Type] = router.New(s.Port, s.Mode)
		return s.webs[s.Type].Set().Run()
	}
	return s
}

func (s *Server) Run() error {
	if s.stat.IsBegun() {
		return nil
	}
	for k, f := range s.mods {
		_ = helper.Go(f)
		logger.Infor("boot up module['%v']", k)
	}
	s.stat.Set(extern.S_RUNNING)
	logger.Infor("Hi! srv: %+v(%p) is running!", s, s)
	return nil
}

func (s *Server) Del() error {
	if s.stat.IsEnded() {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if service.H != nil {
		service.H.StopSnapWorkers()
	}
	s.stat.Set(extern.S_DELTING)
	defer s.stat.Set(extern.S_DELETED)
	for _, r := range s.webs {
		if err := r.Del(); err != nil {
			s.stat.Set(extern.E_DELTING)
			logger.Error("Er! del srv: %+v(%p) failed! err='%v'", s, s, err)
			continue
		}
	}
	if s.stat.IsError() {
		return errors.Errorf("Er! del srv:%+v(%p) failed!", s, s)
	}
	logger.Infor("Hi! del srv: %+v(%p) success!", s, s)
	return nil
}
