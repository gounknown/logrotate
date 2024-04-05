package logrotate

import (
	"fmt"
	"log"
	"os"
)

func ExampleNew() {
	l, _ := New(
		"logs/test.log",
		WithMaxSize(10), // 10 bytes
	)
	log.SetOutput(l)

	log.Printf("Hello, World!")
	log.Printf("Hello, World!")
	log.Printf("Hello, World!")

	files, _ := os.ReadDir("logs")
	for _, file := range files {
		fmt.Println(file.Name())
	}
	os.RemoveAll("logs")

	// OUTPUT:
	// test.log
	// test.log.1
	// test.log.2
}
