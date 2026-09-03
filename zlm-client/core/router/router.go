package router

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
	"zlm-admin/middleware"

	"github.com/gin-gonic/gin"
)

var WebFS fs.FS

type Router struct {
	port   string
	mode   string
	engine *gin.Engine
	server *http.Server
	tlsSrv *http.Server
}

func (r *Router) Set() *Router {
	e := r.engine
	_ = e.SetTrustedProxies(nil)
	middleware.AddPlugins(e)
	return r.SetupLog().SetupWeb().SetupApp()
}

func (r *Router) Run() (err error) {
	if r.tlsSrv != nil {
		go func() {
			logger.Infor("https listen %s", r.tlsSrv.Addr)
			if e := r.tlsSrv.ListenAndServeTLS("", ""); e != nil && e != http.ErrServerClosed {
				logger.Error("@https: %v\n", e)
			}
		}()
	}
	logger.Infor("http listen %s", r.server.Addr)
	if err := r.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("@router: %v\n", err)
		return err
	}
	return nil
}

func (r *Router) Del() (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if r.tlsSrv != nil {
		_ = r.tlsSrv.Shutdown(ctx)
	}
	if err = r.server.Shutdown(ctx); err != nil {
		logger.Error("@router: %v\n", err)
		return err
	}
	return nil
}

func New(port, mode string) *Router {
	gin.ForceConsoleColor()
	gin.SetMode(mode)
	e := gin.New()
	h := e.Handler()
	s := &http.Server{
		Addr:              ":" + port,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	r := &Router{port: port, mode: mode, engine: e, server: s}
	if tlsCfg := loadAdminTLS(); tlsCfg != nil && config.C.Basic.HttpsPort > 0 {
		r.tlsSrv = &http.Server{
			Addr:              fmt.Sprintf(":%d", config.C.Basic.HttpsPort),
			Handler:           h,
			TLSConfig:         tlsCfg,
			ErrorLog:          tlsErrorLog(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
	}
	return r
}
