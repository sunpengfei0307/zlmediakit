package main

import (
	"embed"
	"flag"
	"zlm-admin/core/config"
	"zlm-admin/core/router"
	"zlm-admin/core/server"
	"zlm-admin/utils/helper"
)

//go:embed web
var embeddedWeb embed.FS

func main() {
	cfg := flag.String("config", "", "path to config.toml")
	flag.Parse()
	if *cfg != "" {
		config.Reload(*cfg)
	}
	router.WebFS = embeddedWeb
	helper.Go(func() error {
		return server.New().Set().Run()
	})
	select {}
}
