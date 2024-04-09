package logrotate

import (
	"fmt"
	"log"
	"os"
	"time"
)

func ExampleNew() {
	dir := "_logs/example/"
	defer os.RemoveAll(dir)

	l, _ := New(
		dir+"test.log",
		WithMaxSize(10), // 10 bytes
	)
	log.SetOutput(l)

	log.Printf("Hello, World!") // 13 bytes
	log.Printf("Hello, World!") // 13 bytes
	time.Sleep(time.Second)     // ensure alerady sink to files
	l.Close()

	files, _ := os.ReadDir(dir)
	for _, file := range files {
		fmt.Println(file.Name())
	}

	// OUTPUT:
	// test.log
	// test.log.1
	// test.log.2
}
