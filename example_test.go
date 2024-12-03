package logrotate

import (
	"fmt"
	"log"
	"os"
	"time"
)

func ExampleNew() {
	dir := "_logs/example/"
	// defer os.RemoveAll(dir)

	l, _ := New(
		dir+"test.log",
		WithMaxSize(30), // 30 bytes (with 20 bytes timestamp ahea)
	)
	log.SetOutput(l)

	log.Printf("logrotate")     // 9 bytes
	log.Printf("Hello, World!") // 13 bytes
	time.Sleep(time.Second)     // ensure already sink to files
	l.Close()

	files, _ := os.ReadDir(dir)
	for _, file := range files {
		fmt.Println(file.Name())
	}

	// OUTPUT:
	// test.log
	// test.log.1
}
