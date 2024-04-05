package logrotate

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func ExampleNew() {
	logDir := filepath.Join(os.TempDir(), "logrotate-example")
	logPath := filepath.Join(logDir, "test.log")

	for i := 0; i < 2; i++ {
		writer, err := New(logPath,
			WithMaxSize(4),
		)
		if err != nil {
			panic(err)
		}

		n, err := writer.Write([]byte("test"))
		if err != nil || n != 4 {
			log.Fatalf("write error: %s, write count: %d", err, n)
		}
		n, err = writer.Write([]byte("test"))
		if err != nil || n != 4 {
			log.Fatalf("write error: %s, write count: %d", err, n)
		}
		err = writer.Close()
		if err != nil {
			panic(err)
		}
	}

	files, err := os.ReadDir(logDir)
	if err != nil {
		panic(err)
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			panic(err)
		}
		fmt.Println(file.Name(), info.Size())
	}

	err = os.RemoveAll(logDir)
	if err != nil {
		panic(err)
	}
	// OUTPUT:
	// test.log 8
	// test.log.1 4
	// test.log.2 4
}
