package logrotate

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/lestrrat-go/strftime"
	"github.com/pkg/errors"
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

// genFilename creates a file name based on the pattern, the current time, and the
// rotation time.
//
// The bsase time that is used to generate the filename is truncated based
// on the rotation time.
func genFilename(pattern *strftime.Strftime, clock Clock, rotationTime time.Duration) string {
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
		base = base.Truncate(rotationTime)
		base = time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), base.Minute(), base.Second(), base.Nanosecond(), base.Location())
	} else {
		base = now.Truncate(rotationTime)
	}

	return pattern.FormatString(base)
}

// createFile creates a new file in the given path, creating parent directories
// as necessary
func createFile(filename string) (*os.File, error) {
	// make sure the dir is existed, eg:
	// ./foo/bar/baz/hello.log must make sure ./foo/bar/baz is existed
	dirname := filepath.Dir(filename)
	if err := os.MkdirAll(dirname, 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to create directory %s", dirname)
	}
	// if we got here, then we need to create a file
	fh, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, errors.Errorf("failed to open file %s: %s", filename, err)
	}

	return fh, nil
}

var patternConversionRegexps = []*regexp.Regexp{
	regexp.MustCompile(`%[%+A-Za-z]`),
	regexp.MustCompile(`\*+`),
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
