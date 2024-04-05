package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/lestrrat-go/strftime"
)

// Clock is a source of time for logrotate.
type Clock interface {
	// Now returns the current local time.
	Now() time.Time
}

// DefaultClock is the default clock used by logrotate in operations that
// require time. This clock uses the system clock for all operations.
var DefaultClock = systemClock{}

// systemClock implements default Clock that uses system time.
type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

// genFilename creates a file name based on pattern, clock, and maxInterval.
//
// The base time used to generate the filename is truncated based
// on the max interval.
func genFilename(pattern *strftime.Strftime, clock Clock, maxInterval time.Duration) string {
	now := clock.Now()
	// XXX HACK: Truncate only happens in UTC semantics, apparently.
	// observed values for truncating given time with 86400 secs:
	//
	// before truncation: 2018/06/01 03:54:54 2018-06-01T03:18:00+09:00
	// after  truncation: 2018/06/01 03:54:54 2018-05-31T09:00:00+09:00
	//
	// This is really annoying when we want to truncate in local time
	// so we hack: we take the apparent local time in the local zone,
	// and pretend that it's in UTC. do our math, and put it back to
	// the local zone
	var base time.Time
	if now.Location() != time.UTC {
		base = time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), time.UTC)
		base = base.Truncate(maxInterval)
		base = time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), base.Minute(), base.Second(), base.Nanosecond(), base.Location())
	} else {
		base = now.Truncate(maxInterval)
	}

	return pattern.FormatString(base)
}

// createFile creates a new file in the given path, creating parent directories
// as necessary
func createFile(filename string) (*os.File, error) {
	// make sure the parent dir is existed, e.g.:
	// ./foo/bar/baz/hello.log must make sure ./foo/bar/baz is existed
	dirname := filepath.Dir(filename)
	if err := os.MkdirAll(dirname, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for new logfile: %s", err)
	}
	// if we got here, then we need to create a file
	fh, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open new logfile %s: %s", filename, err)
	}

	return fh, nil
}

var patternConversionRegexps = []*regexp.Regexp{
	regexp.MustCompile(`%[%+A-Za-z]`), // strftime format pattern
	regexp.MustCompile(`\*+`),         // one or multiple *
}

func parseGlobPattern(pattern string) string {
	globPattern := pattern
	for _, re := range patternConversionRegexps {
		globPattern = re.ReplaceAllString(globPattern, "*")
	}
	return globPattern
}

// tracef formats according to a format specifier and writes to w
// with trace info and a newline appended.
func tracef(w io.Writer, format string, args ...any) (int, error) {
	pc := make([]uintptr, 15)
	n := runtime.Callers(2, pc)
	frames := runtime.CallersFrames(pc[:n])
	frame, _ := frames.Next()

	traceArgs := []any{
		filepath.Base(frame.File),
		frame.Line,
		filepath.Base(frame.Function),
	}
	args = append(traceArgs, args...)
	return fmt.Fprintf(w, "%s:%d %s "+format+"\n", args...)
}

type cleanupGuard struct {
	enable bool
	fn     func()
	mutex  sync.Mutex
}

func (g *cleanupGuard) Enable() {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.enable = true
}

func (g *cleanupGuard) Run() {
	g.fn()
}
