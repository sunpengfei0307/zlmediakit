package controler

import (
	"html/template"
	"io/fs"
	"zlm-admin/core/logger"

	"github.com/gin-gonic/gin"
)

func LoadHTML(e *gin.Engine, webFS fs.FS) error {
	sub, err := fs.Sub(webFS, "web/templates")
	if err != nil {
		return err
	}
	t, err := template.New("").Funcs(tmplFuncs()).ParseFS(sub, "*.html")
	if err != nil {
		return err
	}
	e.SetHTMLTemplate(t)
	logger.Infor("html templates loaded")
	return nil
}
