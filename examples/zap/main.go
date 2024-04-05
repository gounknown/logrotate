package main

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gounknown/logrotate"
)

func main() {
	// logrotate is already safe for concurrent use, so we don't need to
	// lock it.
	l, err := logrotate.New(
		"logs/app.%Y%m%d%H.log",
		logrotate.WithLinkName("logs/app"),
		logrotate.WithMaxAge(24*time.Hour),
		logrotate.WithMaxSize(10),
		logrotate.WithMaxInterval(time.Hour),
	)
	if err != nil {
		panic(err)
	}

	w := zapcore.AddSync(l)
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		w,
		zap.InfoLevel,
	)
	logger := zap.New(core)
	logger.Info("Hello, World1!")
	logger.Info("Hello, World2!")
}
