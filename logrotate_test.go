package logrotate

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
)

func TestLogRotate(t *testing.T) {
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

				return append(options, WithLinkName(linkName))
			},
			CheckExtras: func(t *testing.T, r *Logger, dir string) bool {
				linkName := filepath.Join(dir, "log")
				linkDest, err := os.Readlink(linkName)
				if !assert.NoError(t, err, `os.Readlink(%#v) should succeed`, linkName) {
					return false
				}

				expectedLinkDest := filepath.Base(r.CurrentFileName())
				t.Logf("expecting relative link: %s", expectedLinkDest)

				return assert.Equal(t, linkDest, expectedLinkDest, `Symlink destination should  match expected filename (%#v != %#v)`, expectedLinkDest, linkDest)
			},
		},
		{
			Name: "With Symlink (multiple levels)",
			FixArgs: func(options []Option, dir string) []Option {
				linkName := filepath.Join(dir, "nest1", "nest2", "log")

				return append(options, WithLinkName(linkName))
			},
			CheckExtras: func(t *testing.T, r *Logger, dir string) bool {
				linkName := filepath.Join(dir, "nest1", "nest2", "log")
				linkDest, err := os.Readlink(linkName)
				if !assert.NoError(t, err, `os.Readlink(%#v) should succeed`, linkName) {
					return false
				}

				expectedLinkDest := filepath.Join("..", "..", filepath.Base(r.CurrentFileName()))
				t.Logf("expecting relative link: %s", expectedLinkDest)

				return assert.Equal(t, linkDest, expectedLinkDest, `Symlink destination should  match expected filename (%#v != %#v)`, expectedLinkDest, linkDest)
			},
		},
	}

	for i, tc := range testCases {
		i := i   // avoid lint errors
		tc := tc // avoid lint errors
		t.Run(tc.Name, func(t *testing.T) {
			dir := filepath.Join(os.TempDir(), fmt.Sprintf("logrotate-test%d", i))
			defer os.RemoveAll(dir)

			// Change current time, so we can safely purge old logs
			dummyTime := time.Now().Add(-7 * 24 * time.Hour)
			dummyTime = dummyTime.Add(time.Duration(-1 * dummyTime.Nanosecond()))
			clock := clockwork.NewFakeClockAt(dummyTime)

			options := []Option{WithClock(clock), WithMaxAge(24 * time.Hour)}
			if fn := tc.FixArgs; fn != nil {
				options = fn(options, dir)
			}

			r, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"), options...)
			if !assert.NoError(t, err, `New should succeed`) {
				return
			}
			defer r.Close()

			str := "Hello, World"
			n, err := r.Write([]byte(str))
			if !assert.NoError(t, err, "r.Write should succeed") {
				return
			}

			if !assert.Len(t, str, n, "r.Write should succeed") {
				return
			}

			fn := r.CurrentFileName()
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
			r.Write([]byte(str))
			newfn := r.CurrentFileName()
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

			time.Sleep(100 * time.Millisecond)

			// fn was declared above, before mocking CurrentTime
			// Old files should have been unlinked
			_, err = os.Stat(fn)
			if !assert.Error(t, err, "os.Stat should have failed") {
				return
			}

			if fn := tc.CheckExtras; fn != nil {
				if !fn(t, r, dir) {
					return
				}
			}
		})
	}
}

func CreateRotationTestFile(dir string, base time.Time, d time.Duration, n int) {
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

func TestLogMaxBackups(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "logrotate-MaxBackups-test")
	defer os.RemoveAll(dir)

	dummyTime := time.Now().Add(-7 * 24 * time.Hour)
	dummyTime = dummyTime.Add(time.Duration(-1 * dummyTime.Nanosecond()))
	clock := clockwork.NewFakeClockAt(dummyTime)

	t.Run("Either MaxAge or MaxBackups should be set", func(t *testing.T) {
		r, err := New(
			filepath.Join(dir, "log%Y%m%d%H%M%S"),
			WithClock(clock),
			WithMaxAge(time.Duration(0)),
			WithMaxBackups(0),
		)
		if !assert.NoError(t, err, `Both of MaxAge and MaxBackups is disabled`) {
			return
		}
		defer r.Close()
	})

	t.Run("Only latest log file is kept", func(t *testing.T) {
		r, err := New(
			filepath.Join(dir, "log%Y%m%d%H%M%S"),
			WithClock(clock),
			WithMaxBackups(1),
		)
		if !assert.NoError(t, err, `New should succeed`) {
			return
		}
		defer r.Close()

		n, err := r.Write([]byte("dummy"))
		if !assert.NoError(t, err, "r.Write should succeed") {
			return
		}
		if !assert.Len(t, "dummy", n, "r.Write should succeed") {
			return
		}
		time.Sleep(100 * time.Millisecond)
		files, _ := filepath.Glob(filepath.Join(dir, "log*"))
		if !assert.Equal(t, 1, len(files), "Only latest log is kept") {
			return
		}
	})

	t.Run("Old log files are purged except 2 log files", func(t *testing.T) {
		CreateRotationTestFile(dir, dummyTime, time.Hour, 5)
		r, err := New(
			filepath.Join(dir, "log%Y%m%d%H%M%S"),
			WithClock(clock),
			WithMaxAge(0),
			WithMaxBackups(2),
		)
		if !assert.NoError(t, err, `New should succeed`) {
			return
		}
		defer r.Close()

		n, err := r.Write([]byte("dummy"))
		if !assert.NoError(t, err, "r.Write should succeed") {
			return
		}
		if !assert.Len(t, "dummy", n, "r.Write should succeed") {
			return
		}
		time.Sleep(100 * time.Millisecond)
		files, _ := filepath.Glob(filepath.Join(dir, "log*"))
		if !assert.Equal(t, 2, len(files), "One file is kept") {
			return
		}
	})
}

func TestLogSetOutput(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "logrotate-test")
	defer os.RemoveAll(dir)

	r, err := New(filepath.Join(dir, "log%Y%m%d%H%M%S"))
	if !assert.NoError(t, err, `New should succeed`) {
		return
	}
	defer r.Close()

	log.SetOutput(r)
	defer log.SetOutput(os.Stderr)

	str := "Hello, World"
	log.Print(str)

	fn := r.CurrentFileName()
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

func TestRotationSuffixSeqNames(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "logrotate-suffix-seq")
	defer os.RemoveAll(dir)

	t.Run("Rotate over unchanged pattern", func(t *testing.T) {
		r, err := New(
			filepath.Join(dir, "unchanged-pattern.log"),
		)
		if !assert.NoError(t, err, `New should succeed`) {
			return
		}

		seen := map[string]struct{}{}
		for i := 0; i < 10; i++ {
			r.Write([]byte("Hello, World!"))
			if !assert.NoError(t, r.Rotate(), "r.Rotate should succeed") {
				return
			}

			// Because every call to Rotate should yield a new log file,
			// and the previous files already exist, the filenames should share
			// the same prefix and have a unique suffix
			fn := filepath.Base(r.CurrentFileName())
			if !assert.True(t, strings.HasPrefix(fn, "unchanged-pattern.log"), "prefix for all filenames should match") {
				return
			}
			r.Write([]byte("Hello, World!"))
			suffix := strings.TrimPrefix(fn, "unchanged-pattern.log")
			expectedSuffix := fmt.Sprintf(".%d", i+1)
			if !assert.True(t, suffix == expectedSuffix, "expected suffix %s found %s", expectedSuffix, suffix) {
				return
			}
			assert.FileExists(t, r.CurrentFileName(), "file does not exist %s", r.CurrentFileName())
			stat, err := os.Stat(r.CurrentFileName())
			if err == nil {
				if !assert.True(t, stat.Size() == 13, "file %s size is %d, expected 13", r.CurrentFileName(), stat.Size()) {
					return
				}
			} else {
				assert.Failf(t, "could not stat file %s", r.CurrentFileName())

				return
			}

			if _, ok := seen[suffix]; !assert.False(t, ok, `filename suffix %s should be unique`, suffix) {
				return
			}
			seen[suffix] = struct{}{}
		}
		defer r.Close()
	})
	t.Run("Rotate over pattern change over every second", func(t *testing.T) {
		r, err := New(
			filepath.Join(dir, "every-second-pattern-%Y%m%d%H%M%S.log"),
			WithMaxInterval(time.Millisecond),
		)
		if !assert.NoError(t, err, `New should succeed`) {
			return
		}

		for i := 0; i < 5; i++ {
			r.Write([]byte("Hello, World!"))
			if !assert.NoError(t, r.Rotate(), "r.Rotate should succeed") {
				return
			}
			time.Sleep(time.Second)
			// because every new Write should yield a new logfile,
			// every rorate should be create a filename ending with a .1
			if !assert.True(t, strings.HasSuffix(r.CurrentFileName(), ".1"), "log name should end with .1") {
				return
			}
		}
		defer r.Close()
	})
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time {
	return f()
}

func TestGHIssue23(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "logrotate-suffix-seq")
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
				r, err := New(
					filepath.Join(dir, template),
					WithClock(test.Clock), // we're not using WithLocation, but it's the same thing
				)
				if !assert.NoError(t, err, "New should succeed") {
					return
				}

				t.Logf("expected %s", test.Expected)
				r.Rotate()
				if !assert.Equal(t, test.Expected, r.CurrentFileName(), "file names should match") {
					return
				}
			})
		}
	}
}
