package logrotate

import (
	"fmt"
	"log"
	"os"
)

func ExampleNew() {
	l, _ := New(
		"_logs/test.log",
		WithMaxSize(10), // 10 bytes
	)
	log.SetOutput(l)

	log.Printf("Hello, World!") // 13 bytes
	log.Printf("Hello, World!") // 13 bytes

	files, _ := os.ReadDir("_logs")
	for _, file := range files {
		fmt.Println(file.Name())
	}
	os.RemoveAll("_logs")

	// OUTPUT:
	// test.log
	// test.log.1
	// test.log.2
}
