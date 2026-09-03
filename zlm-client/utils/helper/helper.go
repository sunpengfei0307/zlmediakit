package helper

import (
	"fmt"
	"net/url"
	"reflect"
	"runtime"
	"time"
	"zlm-admin/core/logger"

	"github.com/gin-gonic/gin"
)

func GetForm(c *gin.Context) url.Values {
	var params url.Values
	switch c.Request.Method {
	case "GET":
		params, _ = url.ParseQuery(c.Request.URL.RawQuery)
	default:
		_ = c.Request.ParseForm()
		params = c.Request.PostForm
	}
	if params == nil {
		params = url.Values{}
	}
	params["method"] = []string{c.Request.Method}
	return params
}

func Go(f func() error, args ...any) error {
	strFn := FuncName(f)
	exeTv := 0 * time.Millisecond
	if len(args) >= 1 {
		exeTv = args[0].(time.Duration)
	}
	errCh := make(chan error, 1)
	go func(f func() error) {
		defer func() {
			if err := recover(); err != nil {
				errCh <- fmt.Errorf("%v", err)
			}
			close(errCh)
		}()
		begin := time.Now()
		errCh <- f()
		logger.Debug("%v costs: %v", strFn, time.Since(begin))
	}(f)
	if exeTv == 0 {
		return nil
	}
	select {
	case err := <-errCh:
		return err
	case <-time.After(exeTv):
		return fmt.Errorf("func(%v) timeout(>%v)", strFn, exeTv)
	}
}

func PanicErr(err error) {
	if err != nil {
		panic(err)
	}
}

func FuncName(args ...any) string {
	if len(args) == 1 {
		return runtime.FuncForPC(reflect.ValueOf(args[0]).Pointer()).Name()
	}
	pc := make([]uintptr, 1)
	runtime.Callers(2, pc)
	f := runtime.FuncForPC(pc[0])
	return f.Name()
}
