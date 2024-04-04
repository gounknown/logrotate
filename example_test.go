package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
)

func ExampleNew() {
	logDir := filepath.Join(os.TempDir(), "logrotate-example")
	logPath := fmt.Sprintf("%s/test.log", logDir)

	for i := 0; i < 2; i++ {
		writer, err := New(logPath,
			WithMaxSize(4),
		)
		if err != nil {
			fmt.Println("Could not open log file ", err)
			return
		}

		n, err := writer.Write([]byte("test"))
		if err != nil || n != 4 {
			fmt.Println("Write failed ", err, " number written ", n)
			return
		}
		n, err = writer.Write([]byte("test"))
		if err != nil || n != 4 {
			fmt.Println("Write failed ", err, " number written ", n)
			return
		}
		err = writer.Close()
		if err != nil {
			fmt.Println("Close failed ", err)
			return
		}
	}

	files, err := os.ReadDir(logDir)
	if err != nil {
		fmt.Println("ReadDir failed ", err)
		return
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			fmt.Println("get file info failed ", err)
			return
		}
		fmt.Println(file.Name(), info.Size())
	}

	err = os.RemoveAll(logDir)
	if err != nil {
		fmt.Println("RemoveAll failed ", err)
		return
	}
	// OUTPUT:
	// test.log 8
	// test.log.1 4
	// test.log.2 4
}
