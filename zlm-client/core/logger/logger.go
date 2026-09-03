package logger

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"zlm-admin/core/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ZapLog struct {
	Zlog *zap.Logger
	Slog *zap.SugaredLogger
}

var Loggers = map[string]*ZapLog{
	"web": New(filepath.Join(config.LogDir(), "gin.log"), &config.C.Loger),
	"app": New(filepath.Join(config.LogDir(), "zlm-admin.log"), &config.C.Loger),
}

func New(filename string, cfg *config.Loger) *ZapLog {
	syncer := getSyncer(filename, cfg.Size, cfg.Baks, cfg.Ages, cfg.Pack)
	rank := new(zapcore.Level)
	if err := rank.Set(cfg.Rank); err != nil {
		_ = rank.Set("info")
	}
	core := zapcore.NewCore(getFormat(true), syncer, rank)
	zlog := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	wrap := new(ZapLog)
	wrap.Zlog = zlog
	wrap.Slog = zlog.Sugar()
	return wrap
}

func SetSysLog(filename string) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	f, _ := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0744)
	w := io.MultiWriter(os.Stdout, f)
	log.SetOutput(w)
}

func getFormat(isJSON bool) zapcore.Encoder {
	enccfg := zap.NewProductionEncoderConfig()
	enccfg.NameKey = "logger"
	enccfg.EncodeName = zapcore.FullNameEncoder
	enccfg.LevelKey = "level"
	enccfg.EncodeLevel = zapcore.CapitalLevelEncoder
	enccfg.TimeKey = "time"
	enccfg.EncodeTime = zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05.000")
	enccfg.EncodeDuration = zapcore.SecondsDurationEncoder
	enccfg.CallerKey = "caller"
	enccfg.EncodeCaller = func(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(func() string {
			names := strings.Split(caller.Function, "/")
			short := names[len(names)-1]
			if idx := strings.Index(short, "."); idx >= 0 {
				short = short[idx+1:]
			}
			return caller.TrimmedPath() + "<" + short + ">"
		}())
	}
	if isJSON {
		return zapcore.NewJSONEncoder(enccfg)
	}
	return zapcore.NewConsoleEncoder(enccfg)
}

func getSyncer(filename string, maxSize, maxBaks, maxAges int, compress bool) zapcore.WriteSyncer {
	rot := zapcore.AddSync(newRotator(filename, maxSize, maxBaks, maxAges, compress))
	return zapcore.NewMultiWriteSyncer(rot, zapcore.AddSync(os.Stdout))
}

func (lg *ZapLog) Debug(format string, args ...any) { lg.Slog.Debugf(format, args...) }
func (lg *ZapLog) Infor(format string, args ...any) { lg.Slog.Infof(format, args...) }
func (lg *ZapLog) Warnf(format string, args ...any) { lg.Slog.Warnf(format, args...) }
func (lg *ZapLog) Error(format string, args ...any) { lg.Slog.Errorf(format, args...) }
func (lg *ZapLog) Fatal(format string, args ...any) { lg.Slog.Fatalf(format, args...) }
func (lg *ZapLog) Panic(format string, args ...any) { lg.Slog.Panicf(format, args...) }

func Debug(format string, args ...any) { Loggers["app"].Slog.Debugf(format, args...) }
func Infor(format string, args ...any) { Loggers["app"].Slog.Infof(format, args...) }
func Warnf(format string, args ...any) { Loggers["app"].Slog.Warnf(format, args...) }
func Error(format string, args ...any) { Loggers["app"].Slog.Errorf(format, args...) }
func Fatal(format string, args ...any) { Loggers["app"].Slog.Fatalf(format, args...) }
func Panic(format string, args ...any) { Loggers["app"].Slog.Panicf(format, args...) }
