package logrotate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const baseLogDir = "_testlogs"

// Common initialization function
func setup() {
	// Perform common setup tasks here
	fmt.Println("Common setup tasks")
	os.RemoveAll(baseLogDir)
}

// Common cleanup function
func teardown() {
	// Perform common cleanup tasks here
	fmt.Println("Common cleanup tasks")
}

// TestMain is the entry point for the tests
func TestMain(m *testing.M) {
	// Call the setup function
	setup()

	// Run the tests
	exitCode := m.Run()

	// Call the teardown function
	teardown()

	// Exit with the appropriate code
	os.Exit(exitCode)
}

// go test -bench ^Benchmark_MaxBackups1000$ -benchmem -benchtime=10s -cpuprofile=profile.out
// go tool pprof -http=:8080 profile.out
func Benchmark_MaxBackups1000(b *testing.B) {
	dir := filepath.Join(baseLogDir, "Benchmark_MaxBackups1000")
	defer os.RemoveAll(dir)
	l, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"),
		WithSymlink(filepath.Join(dir, "log")),
		WithMaxSize(10),
		// WithMaxAge(3*time.Second),
		WithMaxBackups(1000),
	)
	require.NoError(b, err, "New should succeed")
	defer l.Close()

	log.SetOutput(l)

	logline := "Hello, World"
	for i := 0; i < b.N; i++ {
		log.Println(logline)
	}
	// time.Sleep(time.Second)
}

func Benchmark_MaxInterval(b *testing.B) {
	dir := filepath.Join(baseLogDir, "Benchmark_MaxInterval")
	defer os.RemoveAll(dir)
	l, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"),
		WithSymlink(filepath.Join(dir, "log")),
		WithMaxSize(10),
		WithMaxInterval(time.Second),
		// WithMaxAge(3*time.Second),
		WithMaxBackups(10),
		WithWriteChan(100),
	)
	require.NoError(b, err, "New should succeed")
	defer l.Close()

	log.SetOutput(l)

	logline := "Hello, World"
	for i := 0; i < b.N; i++ {
		log.Println(logline)
	}
}

var logline50 = []byte("01234567890123456789012345678901234567890123456789")

func Benchmark_WriteWithoutRotate(b *testing.B) {
	dir := filepath.Join(baseLogDir, "BenchmarkNoRotate")
	defer os.RemoveAll(dir)
	l, err := New(filepath.Join(dir, "log"),
		WithMaxSize(0),
	)
	require.NoError(b, err, "New should succeed")
	defer l.Close()

	for i := 0; i < b.N; i++ {
		n, err := l.Write(logline50)
		require.NoError(b, err, "Write should succeed")
		require.Equal(b, len(logline50), n, "Write length should match")
	}
}

func Benchmark_BufferedWriteWithoutRotate(b *testing.B) {
	dir := filepath.Join(baseLogDir, "BenchmarkNoRotate")
	defer os.RemoveAll(dir)
	l, err := New(filepath.Join(dir, "log"),
		WithMaxSize(0),
		WithWriteChan(100),
	)
	require.NoError(b, err, "New should succeed")
	defer l.Close()

	for i := 0; i < b.N; i++ {
		n, err := l.Write(logline50)
		require.NoError(b, err, "Write should succeed")
		require.Equal(b, len(logline50), n, "Write length should match")
	}
}

func Test_Rotate(t *testing.T) {
	testCases := []struct {
		Name        string
		FixArgs     func([]Option, string) []Option
		CheckExtras func(*testing.T, *Logger, string) bool
	}{
		{
			Name: "Basic Usage",
		},
		{
			Name: "With Symlink",
			FixArgs: func(options []Option, dir string) []Option {
				linkName := filepath.Join(dir, "log")

				return append(options, WithSymlink(linkName))
			},
			CheckExtras: func(t *testing.T, l *Logger, dir string) bool {
				linkName := filepath.Join(dir, "log")
				linkDest, err := os.Readlink(linkName)
				if !assert.NoError(t, err, `os.Readlink(%#v) should succeed`, linkName) {
					return false
				}

				expectedLinkDest := filepath.Base(l.currentFilename())
				t.Logf("expecting relative link: %s", expectedLinkDest)

				return assert.Equal(t, linkDest, expectedLinkDest, `Symlink destination should  match expected filename (%#v != %#v)`, expectedLinkDest, linkDest)
			},
		},
		{
			Name: "With Symlink (multiple levels)",
			FixArgs: func(options []Option, dir string) []Option {
				linkName := filepath.Join(dir, "nest1", "nest2", "log")

				return append(options, WithSymlink(linkName))
			},
			CheckExtras: func(t *testing.T, l *Logger, dir string) bool {
				linkName := filepath.Join(dir, "nest1", "nest2", "log")
				linkDest, err := os.Readlink(linkName)
				if !assert.NoError(t, err, `os.Readlink(%#v) should succeed`, linkName) {
					return false
				}

				expectedLinkDest := filepath.Join("..", "..", filepath.Base(l.currentFilename()))
				t.Logf("expecting relative link: %s", expectedLinkDest)

				return assert.Equal(t, linkDest, expectedLinkDest, `Symlink destination should  match expected filename (%#v != %#v)`, expectedLinkDest, linkDest)
			},
		},
	}

	for i, tc := range testCases {
		i := i   // avoid lint errors
		tc := tc // avoid lint errors
		t.Run(tc.Name, func(t *testing.T) {
			dir := filepath.Join(baseLogDir, fmt.Sprintf("Test_Rotate-%d", i))
			defer os.RemoveAll(dir)

			// Change current time, so we can safely purge old logs
			dummyTime := time.Now().Add(-7 * 24 * time.Hour)
			dummyTime = dummyTime.Add(time.Duration(-1 * dummyTime.Nanosecond()))
			clock := clockwork.NewFakeClockAt(dummyTime)

			options := []Option{WithClock(clock), WithMaxAge(24 * time.Hour)}
			if fn := tc.FixArgs; fn != nil {
				options = fn(options, dir)
			}

			l, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"), options...)
			require.NoError(t, err, `New should succeed`)
			defer l.Close()

			str := "Hello, World"
			n, err := l.Write([]byte(str))
			require.NoError(t, err, "l.Write should succeed")
			require.Len(t, str, n, "l.Write should succeed")

			time.Sleep(100 * time.Millisecond)
			fn := l.currentFilename()
			if fn == "" {
				t.Errorf("Could not get filename %s", fn)
			}

			content, err := os.ReadFile(fn)
			if err != nil {
				t.Errorf("Failed to read file %s: %s", fn, err)
			}

			if string(content) != str {
				t.Errorf(`File content does not match (was "%s")`, content)
			}

			err = os.Chtimes(fn, dummyTime, dummyTime)
			if err != nil {
				t.Errorf("Failed to change access/modification times for %s: %s", fn, err)
			}

			fi, err := os.Stat(fn)
			if err != nil {
				t.Errorf("Failed to stat %s: %s", fn, err)
			}

			if !fi.ModTime().Equal(dummyTime) {
				t.Errorf("Failed to chtime for %s (expected %s, got %s)", fn, fi.ModTime(), dummyTime)
			}

			clock.Advance(7 * 24 * time.Hour)

			// This next Write() should trigger Rotate()
			l.Write([]byte(str))
			time.Sleep(100 * time.Millisecond)
			newfn := l.currentFilename()
			if newfn == fn {
				t.Errorf(`New file name and old file name should not match ("%s" != "%s")`, fn, newfn)
			}

			content, err = os.ReadFile(newfn)
			if err != nil {
				t.Errorf("Failed to read file %s: %s", newfn, err)
			}

			if string(content) != str {
				t.Errorf(`File content does not match (was "%s")`, content)
			}
			// wait for mill background goroutine
			time.Sleep(100 * time.Millisecond)

			// fn was declared above, before mocking CurrentTime
			// Old files should have been unlinked
			_, err = os.Stat(fn)
			require.Error(t, err, "os.Stat should have failed")

			if fn := tc.CheckExtras; fn != nil {
				if !fn(t, l, dir) {
					return
				}
			}
		})
	}
}

func Test_MaxInterval0(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Benchmark_MaxInterval0")
	defer os.RemoveAll(dir)
	l, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"),
		WithSymlink(filepath.Join(dir, "log")),
		WithMaxSize(10),
		WithMaxInterval(0),
	)
	require.NoError(t, err, `New should succeed`)
	for i := 0; i < 10; i++ {
		l.Write([]byte("Hello, World"))
	}
}

func Test_BufferedWrite(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_BufferedWrite")
	defer os.RemoveAll(dir)

	l, err := New(
		filepath.Join(dir, "log"),
		WithMaxSize(10),
		WithWriteChan(100),
	)
	require.NoError(t, err, `New should succeed`)
	for i := 0; i < 10; i++ {
		l.Write([]byte("Hello, World"))
	}
	time.Sleep(1 * time.Second)
	for i := 0; i < 10; i++ {
		l.Write([]byte("Hello, World"))
	}
	l.Close()
	time.Sleep(1 * time.Second)
	files, _ := filepath.Glob(filepath.Join(dir, "log*"))
	require.GreaterOrEqual(t, len(files), 20, "count of rotated log files is wrong")
}

func Test_MaxBackups(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_MaxBackups")
	defer os.RemoveAll(dir)

	dummyTime := time.Now().Add(-7 * 24 * time.Hour)
	dummyTime = dummyTime.Add(time.Duration(-1 * dummyTime.Nanosecond()))
	clock := clockwork.NewFakeClockAt(dummyTime)

	t.Run("Either MaxAge or MaxBackups should be set", func(t *testing.T) {
		l, err := New(
			filepath.Join(dir, "log%Y%m%d%H%M%S"),
			WithClock(clock),
			WithMaxAge(time.Duration(0)),
			WithMaxBackups(0),
		)
		require.NoError(t, err, `Both of MaxAge and MaxBackups is disabled`)
		defer l.Close()
	})

	t.Run("Only latest log file is kept", func(t *testing.T) {
		l, err := New(
			filepath.Join(dir, "log%Y%m%d%H%M%S"),
			WithClock(clock),
			WithMaxBackups(1),
		)
		require.NoError(t, err, `New should succeed`)
		defer l.Close()

		n, err := l.Write([]byte("dummy"))
		require.NoError(t, err, "l.Write should succeed")
		require.Len(t, "dummy", n, "l.Write should succeed")

		time.Sleep(100 * time.Millisecond)
		files, _ := filepath.Glob(filepath.Join(dir, "log*"))
		require.Equal(t, 1, len(files), "Only latest log is kept")
	})

	createRotationTestFile := func(dir string, base time.Time, d time.Duration, n int) {
		timestamp := base
		for i := 0; i < n; i++ {
			// %Y%m%d%H%M%S
			suffix := timestamp.Format("20060102150405")
			path := filepath.Join(dir, "log"+suffix)
			os.WriteFile(path, []byte("rotation test file\n"), os.ModePerm)
			os.Chtimes(path, timestamp, timestamp)
			timestamp = timestamp.Add(d)
		}
	}
	t.Run("Old log files are purged except 2 log files", func(t *testing.T) {
		createRotationTestFile(dir, dummyTime, time.Hour, 5)
		l, err := New(
			filepath.Join(dir, "log%Y%m%d%H%M%S"),
			WithClock(clock),
			WithMaxAge(0),
			WithMaxBackups(2),
		)
		require.NoError(t, err, `New should succeed`)
		defer l.Close()

		n, err := l.Write([]byte("dummy"))
		require.NoError(t, err, "l.Write should succeed")
		require.Len(t, "dummy", n, "l.Write should succeed")

		time.Sleep(100 * time.Millisecond)
		files, _ := filepath.Glob(filepath.Join(dir, "log*"))
		require.Equal(t, 2, len(files), "One file is kept")
	})
}

func Test_SetOutput(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_SetOutput")
	defer os.RemoveAll(dir)

	l, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"))
	require.NoError(t, err, `New should succeed`)
	defer l.Close()

	log.SetOutput(l)
	defer log.SetOutput(os.Stderr)

	str := "Hello, World"
	log.Print(str)
	time.Sleep(100 * time.Millisecond)
	fn := l.currentFilename()
	if fn == "" {
		t.Errorf("Could not get filename %s", fn)
	}

	content, err := os.ReadFile(fn)
	if err != nil {
		t.Errorf("Failed to read file %s: %s", fn, err)
	}

	if !strings.Contains(string(content), str) {
		t.Errorf(`File content does not contain "%s" (was "%s")`, str, content)
	}
}

func Test_RotationSuffixSeq(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_RotationSuffixSeq")
	defer os.RemoveAll(dir)

	t.Run("Rotate over unchanged pattern", func(t *testing.T) {
		l, err := New(
			filepath.Join(dir, "unchanged-pattern.log"),
		)
		require.NoError(t, err, `New should succeed`)

		seen := map[string]struct{}{}
		for i := 0; i < 5; i++ {
			l.Write([]byte("Hello, World!"))
			time.Sleep(10 * time.Millisecond)
			require.NoError(t, l.Rotate(), "l.Rotate should succeed")
			// Because every call to Rotate should yield a new log file,
			// and the previous files already exist, the filenames should share
			// the same prefix and have a unique suffix
			fn := filepath.Base(l.currentFilename())
			require.True(t, strings.HasPrefix(fn, "unchanged-pattern.log"), "prefix for all filenames should match")
			l.Write([]byte("Hello, World!"))
			time.Sleep(10 * time.Millisecond)
			suffix := strings.TrimPrefix(fn, "unchanged-pattern.log")
			expectedSuffix := fmt.Sprintf(".%d", i+1)
			require.True(t, suffix == expectedSuffix, "expected suffix %s found %s", expectedSuffix, suffix)
			require.FileExists(t, l.currentFilename(), "file does not exist %s", l.currentFilename())

			stat, err := os.Stat(l.currentFilename())
			require.NoError(t, err, "could not stat file %s", l.currentFilename())
			require.True(t, stat.Size() == 13, "file %s size is %d, expected 13", l.currentFilename(), stat.Size())

			_, ok := seen[suffix]
			require.False(t, ok, `filename suffix %s should be unique`, suffix)
			seen[suffix] = struct{}{}
		}
		defer l.Close()
	})
	t.Run("Rotate over pattern change over every second", func(t *testing.T) {
		pattern := filepath.Join(dir, "every-second-pattern-%Y%m%d%H%M%S.log")
		l, err := New(
			pattern,
			WithMaxInterval(time.Second),
		)
		require.NoError(t, err, `New should succeed`)
		defer l.Close()

		l.Write([]byte("init")) // first write
		for i := 0; i < 5; i++ {
			time.Sleep(time.Second)
			l.Write([]byte("Hello, World!"))
			require.True(t, strings.HasSuffix(l.currentFilename(), ".log"), "log name should end with .log")
			require.NoError(t, l.Rotate(), "l.Rotate should succeed")
			// because every new Write should yield a new log file,
			// every rotate should create a filename ending with a .1
			require.True(t, strings.HasSuffix(l.currentFilename(), ".1"), "log name should end with .1")
		}
	})
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time {
	return f()
}

func Test_TimeZone(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_TimeZone")
	defer os.RemoveAll(dir)

	for _, locName := range []string{"Asia/Tokyo", "Pacific/Honolulu"} {
		locName := locName
		loc, _ := time.LoadLocation(locName)
		tests := []struct {
			Expected string
			Clock    Clock
		}{
			{
				Expected: filepath.Join(dir, strings.ToLower(strings.Replace(locName, "/", "_", -1))+".201806010000.log"),
				Clock: ClockFunc(func() time.Time {
					return time.Date(2018, 6, 1, 3, 18, 0, 0, loc)
				}),
			},
			{
				Expected: filepath.Join(dir, strings.ToLower(strings.Replace(locName, "/", "_", -1))+".201712310000.log"),
				Clock: ClockFunc(func() time.Time {
					return time.Date(2017, 12, 31, 23, 52, 0, 0, loc)
				}),
			},
		}
		for _, test := range tests {
			test := test
			t.Run(fmt.Sprintf("location = %s, time = %s", locName, test.Clock.Now().Format(time.RFC3339)), func(t *testing.T) {
				template := strings.ToLower(strings.Replace(locName, "/", "_", -1)) + ".%Y%m%d%H%M.log"
				l, err := New(
					filepath.Join(dir, template),
					WithClock(test.Clock), // we're not using WithLocation, but it's the same thing
				)
				require.NoError(t, err, "New should succeed")

				t.Logf("expected %s", test.Expected)
				l.Rotate()
				require.Equal(t, test.Expected, l.currentFilename(), "file names should match")
			})
		}
	}
}

func Test_CreateNewFileWhenRemovedOnWrite(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_CreateNewFileWhenRemovedOnWrite")
	defer os.RemoveAll(dir)
	l, err := New(
		filepath.Join(dir, "app.%Y%m%d%H.log"),
		WithSymlink(filepath.Join(dir, "app")),
	)
	require.NoError(t, err, "New should succeed")

	l.Write([]byte("before removed"))
	time.Sleep(time.Second)
	os.RemoveAll(dir)
	l.Write([]byte("after removed"))
	files, _ := os.ReadDir(dir)
	// symlink may alse be created
	require.LessOrEqual(t, 1, len(files), "should auto create new log files after removed")

	err = l.Close()
	require.NoError(t, err, "Close should succeed")
	time.Sleep(100 * time.Millisecond)
	files, _ = os.ReadDir(dir)
	require.Equal(t, 2, len(files), "should auto create new log files after removed")
}

func Test_DiscardsWithWriteChan(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_DiscardsWithWriteChan")
	defer os.RemoveAll(dir)
	l, err := New(
		filepath.Join(dir, "app.%Y%m%d%H.log"),
		WithSymlink(filepath.Join(dir, "app")),
		WithWriteChan(1),
	)
	require.NoError(t, err, "New should succeed")

	log.SetOutput(l)

	for i := 0; i < 1000; i++ {
		go func() {
			log.Println(logline50)
		}()
	}
	metrics := l.Metrics()
	require.Greaterf(t, metrics.Discards, uint64(0), "Discarded log lines (%d) should be >= 1", metrics.Discards)
}

func Test_MaxSequence(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_MaxSequence")
	// defer os.RemoveAll(dir)
	l, err := New(
		filepath.Join(dir, "app.%Y%m%d%H.log"),
		WithMaxSize(1),
		WithMaxSequence(10),
	)
	require.NoError(t, err, "New should succeed")

	for i := 0; i < 1000; i++ {
		line := fmt.Sprintf("%d: %v", i, time.Now())
		l.Write([]byte(line))
	}
	err = l.Close()
	require.NoError(t, err, "Close should succeed")
	time.Sleep(100 * time.Millisecond)
	files, _ := os.ReadDir(dir)
	require.Equal(t, 11, len(files), "should auto create new log files after removed")

	l, err = New(
		filepath.Join(dir, "app.%Y%m%d%H.log"),
		WithMaxSize(100),
		WithMaxSequence(10),
	)
	require.NoError(t, err, "New should succeed")

	for i := 1000; i < 2000; i++ {
		line := fmt.Sprintf("%d: %v", i, time.Now())
		l.Write([]byte(line))
	}
	err = l.Close()
	require.NoError(t, err, "Close should succeed")
	time.Sleep(100 * time.Millisecond)
	files, _ = os.ReadDir(dir)
	require.Equal(t, 11, len(files), "should auto create new log files after removed")
}

func Test_SymlinkTologfileWithSuffix(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_SymlinkTologfileWithSuffix")
	defer os.RemoveAll(dir)
	symlinkFilePath := filepath.Join(dir, "app")
	l, err := New(
		filepath.Join(dir, "app.%Y%m%d%H.log"),
		WithSymlink(symlinkFilePath),
		WithMaxSize(8),
	)
	require.NoError(t, err, "New should succeed")

	l.Write([]byte("logfile1"))
	l.Write([]byte("logfile2"))
	l.Write([]byte("logfile3"))

	time.Sleep(100 * time.Millisecond)
	files, _ := os.ReadDir(dir)
	require.Equal(t, 4, len(files), "should create 1 symlink file and 3 log files")

	fileContent, err := os.ReadFile(symlinkFilePath)
	require.NoError(t, err, "ReadFile should succeed")
	require.Equal(t, fileContent, []byte("logfile3"), "symlink file should auto link to the 3rd log file")
	err = l.Close()
	require.NoError(t, err, "Close should succeed")
}

func Test_Stat_ErrPermission(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_Stat_ErrPermission")
	defer os.RemoveAll(dir)

	l, err := New(
		filepath.Join(dir, "app.log"),
	)
	require.NoError(t, err, "New should succeed")
	l.Write([]byte("1"))
	// hook l.osStat
	l.osStat = func(string) (os.FileInfo, error) {
		return nil, fs.ErrPermission
	}
	_, err = l.Write([]byte("2"))
	require.Equal(t, true, errors.Is(err, fs.ErrPermission), "Should return fs.ErrPermission error")
	// restored
	l.osStat = os.Stat
}

func Test_New_OpenExistingOrNew(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_New_OpenExistingOrNew")
	defer os.RemoveAll(dir)

	// New 1
	l, err := New(
		filepath.Join(dir, "app.log"),
		WithMaxSize(10),
	)
	require.NoError(t, err, "New should succeed")
	l.Write([]byte("A"))
	time.Sleep(time.Second)
	err = l.Close()
	require.NoError(t, err, "Close should succeed")

	// New 2
	l, err = New(
		filepath.Join(dir, "app.log"),
		WithMaxSize(10),
	)
	require.NoError(t, err, "New should succeed")
	l.Write([]byte("B"))
	time.Sleep(time.Second)
	err = l.Close()
	require.NoError(t, err, "Close should succeed")

	time.Sleep(100 * time.Millisecond)
	files, _ := os.ReadDir(dir)
	// symlink may alse be created
	require.LessOrEqual(t, 1, len(files), "should auto resume existing log file on New")

	// New 3
	l, err = New(
		filepath.Join(dir, "app.log"),
		WithMaxSize(10),
	)
	require.NoError(t, err, "New should succeed")
	l.Write([]byte("0123456789"))
	time.Sleep(time.Second)
	err = l.Close()
	require.NoError(t, err, "Close should succeed")

	time.Sleep(100 * time.Millisecond)
	files, _ = os.ReadDir(dir)
	// symlink may alse be created
	require.LessOrEqual(t, 2, len(files), "should rotate a new log file on New")
}

type testFile struct {
	werr error // write error
	cerr error // close error
}

func (f testFile) Write(b []byte) (n int, err error) {
	return 0, f.werr
}

func (f testFile) Close() error {
	return f.cerr
}

func Test_Write_Error(t *testing.T) {
	dir := filepath.Join(baseLogDir, "Test_Write_Error")
	defer os.RemoveAll(dir)

	l, err := New(
		filepath.Join(dir, "app.log"),
	)
	require.NoError(t, err, "New should succeed")

	_, err = l.Write([]byte("1"))
	require.NoError(t, err, "Write should succeed")

	// hook l.file
	oldFile := l.file
	l.file = testFile{werr: io.ErrShortWrite}
	_, err = l.Write([]byte("1"))
	require.Equal(t, true, errors.Is(err, io.ErrShortWrite), "Should return error: io.ErrShortWrite")

	// hook l.file
	l.file = testFile{werr: syscall.ENOSPC} // No space left on device
	_, err = l.Write([]byte("1"))
	require.Equal(t, true, errors.Is(err, syscall.ENOSPC), "Should return error: syscall.ENOSPC")

	// hook l.file
	l.file = testFile{werr: io.ErrShortWrite, cerr: fs.ErrClosed}
	_, err = l.Write([]byte("1"))
	require.Equal(t, true, errors.Is(err, fs.ErrClosed), "Should return error: fs.ErrClosed")

	// restored
	l.file = oldFile
}
