package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

// genBaseFilename2 creates a file name based on pattern, clock, and interval.
//
// The base time used to generate the filename is truncated based on interval.
func genBaseFilename2(pattern *strftime.Strftime, clock Clock, interval time.Duration) string {
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
		base = base.Truncate(interval)
		base = time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), base.Minute(), base.Second(), base.Nanosecond(), base.Location())
	} else {
		base = now.Truncate(interval)
	}

	return pattern.FormatString(base)
}

func genBaseFilename(pattern *strftime.Strftime, clock Clock, rotationTime int64) string {
	now := clock.Now()
	_, offset := now.Zone()
	t := time.Unix(rotationTime-int64(offset), 0)
	base := t.In(now.Location())
	return pattern.FormatString(base)
}

// evalCurrRotationTime evaluates the current rotation time in seconds
// at interval scale since the Unix epoch in Location (timezone offset).
func evalCurrRotationTime(clock Clock, tzOffset, interval int64) int64 {
	now := clock.Now().Unix() + tzOffset
	return now - (now % interval)
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

type logfile struct {
	path string
	os.FileInfo
}

// byModTime sorts by file modification time.
type byModTime []*logfile

func (b byModTime) Less(i, j int) bool {
	return b[i].ModTime().After(b[j].ModTime())
}

func (b byModTime) Swap(i, j int) {
	b[i], b[j] = b[j], b[i]
}

func (b byModTime) Len() int {
	return len(b)
}
