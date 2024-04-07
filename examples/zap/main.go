package main

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gounknown/logrotate"
)

func main() {
	// logrotate is safe for concurrent use, so we don't need to lock it.
	l, err := logrotate.New(
		"_logs/app.%Y%m%d%H.log",
		logrotate.WithLinkName("_logs/app"),
		logrotate.WithMaxAge(24*time.Hour),
		logrotate.WithMaxSize(10), // 10 bytes
		logrotate.WithMaxInterval(time.Hour),
	)
	if err != nil {
		panic(err)
	}
	defer l.Close()

	w := zapcore.AddSync(l)
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		w,
		zap.InfoLevel,
	)
	logger := zap.New(core)
	logger.Info("Hello, World!") // over 10 bytes
	logger.Info("Hello, World!") // over 10 bytes
}
