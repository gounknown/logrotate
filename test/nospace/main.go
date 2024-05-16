package main

import (
	"log"
	"time"

	"github.com/gounknown/logrotate"
)

// test "write: No space left on device"
func main() {
	l, err := logrotate.New(
		"_logs/app.%Y%m%d%H%M.log",
		logrotate.WithSymlink("_logs/app"),
		logrotate.WithMaxInterval(time.Minute),
	)
	if err != nil {
		panic(err)
	}
	defer l.Close()

	log.SetOutput(l)

	data := make([]byte, 1024*1024) // 1 MB
	for {
		time.Sleep(time.Second)
		log.Println(data)
	}
}
