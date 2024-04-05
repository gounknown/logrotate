package main

import (
	"log"
	"time"

	"github.com/gounknown/logrotate"
)

func main() {
	l, err := logrotate.New(
		"_logs/app.%Y%m%d%H%M%S.log",
		logrotate.WithMaxInterval(time.Second),
	)
	if err != nil {
		panic(err)
	}
	log.SetOutput(l)

	log.Printf("Hello, World!")
	time.Sleep(time.Second)
	log.Printf("Hello, World!")
}
