// Package logrotate can automatically rotate log files when you write to
// them according to the specified filename pattern and options.
package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/lestrrat-go/strftime"
)

// ensure we always implement io.WriteCloser
var _ io.WriteCloser = (*Logger)(nil)

// Logger is an io.WriteCloser that writes to the appropriate filename. It
// can get automatically rotated as you write to it.
type Logger struct {
	// Read-only fields after *New* method inited.
	opts               *Options
	pattern            *strftime.Strftime
	globPattern        string
	maxIntervalSeconds int64 // max interval in seconds
	tzOffsetSeconds    int64 // time zone offset in seconds

	mu               sync.RWMutex // guards following
	file             *os.File     // current file handle being written to
	size             int64        // write size of current file
	currRotationTime int64        // Unix timestamp with location
	currFilename     string       // current filename being written to
	currBaseFilename string       // base filename without suffix sequence
	currSequence     uint         // filename suffix sequence

	wg     sync.WaitGroup // counts active background goroutines
	millCh chan struct{}  // 1-size notification chan for mill goroutine
	quit   chan struct{}  // closed when writeLoop and millLoop should quit
}

// New creates a new concurrent safe Logger object with the provided
// filename pattern and options.
func New(pattern string, options ...Option) (*Logger, error) {
	globPattern := parseGlobPattern(pattern)

	filenamePattern, err := strftime.New(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid strftime pattern: %v", err)
	}

	opts := parseOptions(options...)

	if opts.maxAge < 0 {
		return nil, fmt.Errorf("option MaxAge cannot be < 0")
	}
	maxIntervalInSeconds := int64(opts.maxInterval.Seconds())
	if maxIntervalInSeconds < 0 {
		return nil, fmt.Errorf("option MaxInterval in seconds cannot be < 0")
	}

	if opts.maxSize < 0 {
		return nil, fmt.Errorf("option MaxSize cannot be < 0")
	}

	_, offset := opts.clock.Now().Zone()

	l := &Logger{
		opts:               opts,
		pattern:            filenamePattern,
		globPattern:        globPattern,
		maxIntervalSeconds: maxIntervalInSeconds,
		tzOffsetSeconds:    int64(offset),
		millCh:             make(chan struct{}, 1),
		quit:               make(chan struct{}),
	}

	// starting the mill goroutine
	l.wg.Add(1)
	go func() {
		l.wg.Done()
		l.millLoop()
	}()

	return l, nil
}

// Write implements io.Writer. It writes to the target file handle that is
// currently being used.
//
// If a write would cause the log file to be larger than MaxSize, or we have
// reached a new rotation time (evaluated based on MaxInterval), the target
// file would get automatically rotated, and old log files would also be purged
// if necessary.
func (l *Logger) Write(p []byte) (n int, err error) {
	// Guard against concurrent writes
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.write(p)
}

func (l *Logger) write(p []byte) (n int, err error) {
	writeLen := int64(len(p))

	// The os.Stat method cost is: 256 B/op, 2 allocs/op
	_, err = os.Stat(l.currFilename)
	if l.file == nil || os.IsNotExist(err) {
		if err = l.openExistingOrNew(writeLen); err != nil {
			return 0, err
		}
	}
	// Factor 1: MaxSize
	if l.opts.maxSize > 0 && l.size+writeLen > int64(l.opts.maxSize) {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	} else {
		// Factor 2: MaxInterval
		if l.maxIntervalSeconds > 0 &&
			l.currRotationTime != evalCurrRotationTime(l.opts.clock, l.tzOffsetSeconds, l.maxIntervalSeconds) {
			if err := l.rotate(); err != nil {
				return 0, err
			}
		}
	}

	n, err = l.file.Write(p)
	if err != nil {
		tracef(os.Stderr, "failed to write: %v, so try to open existing or new file", err)
		if err = l.openExistingOrNew(writeLen); err != nil {
			return 0, err
		}
	}
	l.size += int64(n)

	return n, err
}

// mill performs post-rotation compression and removal of stale log files.
func (l *Logger) mill() {
	select {
	case l.millCh <- struct{}{}:
	default:
	}
}

// millLoop runs in a goroutine to manage post-rotation compression and removal
// of old log files.
func (l *Logger) millLoop() {
	for {
		select {
		case <-l.quit:
			if len(l.millCh) != 0 {
				// what am I going to do, log this?
				_ = l.millRunOnce()
			}
			return
		case <-l.millCh:
			// what am I going to do, log this?
			_ = l.millRunOnce()
		}
	}
}

// millRunOnce performs removal of stale log files. Old log
// files are removed, keeping at most MaxBackups files, as long as
// none of them are older than MaxAge.
func (l *Logger) millRunOnce() error {
	files, err := l.getLogFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	if l.opts.symlink != "" {
		// NOTE: files already sorted by modification time in descending order.
		latestFilename := files[0].path
		if err := link(latestFilename, l.opts.symlink); err != nil {
			return err
		}
	}

	if l.opts.maxBackups == 0 && l.opts.maxAge == 0 {
		return nil
	}

	// TODO: compresess
	var removals []*logfile

	if l.opts.maxAge > 0 {
		var remaining []*logfile
		cutoff := l.opts.clock.Now().Add(-1 * l.opts.maxAge)
		for _, f := range files {
			if f.ModTime().Before(cutoff) {
				removals = append(removals, f)
			} else {
				remaining = append(remaining, f)
			}
		}
		files = remaining
	}

	if l.opts.maxBackups > 0 && l.opts.maxBackups < len(files) {
		preserved := make(map[string]bool)
		for _, f := range files {
			preserved[f.path] = true
			if len(preserved) > l.opts.maxBackups {
				// Only remove if we have more than MaxBackups
				removals = append(removals, f)
			}
		}
	}

	for _, f := range removals {
		// FIXME: need return if encounted an error
		_ = os.Remove(f.path)
	}

	return nil
}

// getLogFiles returns all log files matched the globPattern, sorted by ModTime.
func (l *Logger) getLogFiles() ([]*logfile, error) {
	paths, err := filepath.Glob(l.globPattern)
	if err != nil {
		return nil, err
	}

	logFiles := []*logfile{}
	for _, path := range paths {
		fi, err := os.Lstat(path)
		if err != nil {
			// ignore error
			continue
		}
		if fi.Mode()&os.ModeSymlink == os.ModeSymlink {
			// ignore symlink files
			continue
		}
		logFiles = append(logFiles, &logfile{path, fi})
	}

	sort.Sort(byModTime(logFiles))

	return logFiles, nil
}

// openExistingOrNew opens the logfile if it exists and if the current write
// would not put it over MaxSize. If there is no such file or the write would
// put it over the MaxSize, a new file is created.
func (l *Logger) openExistingOrNew(writeLen int64) error {
	defer l.mill()

	filename := l.evalCurrentFilename(writeLen, false)
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return l.openNew(filename)
	}
	if err != nil {
		return fmt.Errorf("error getting log file info: %s", err)
	}

	if l.opts.maxSize > 0 && info.Size()+writeLen >= int64(l.opts.maxSize) {
		return l.rotate()
	}

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// if we fail to open the old log file for some reason, just ignore
		// it and open a new log file.
		return l.openNew(filename)
	}
	l.file = file
	l.size = info.Size()
	return nil
}

// openNew opens a new log file for writing, moving any old log file out of the
// way.  This methods assumes the file has already been closed.
func (l *Logger) openNew(filename string) error {
	dirname := filepath.Dir(filename)
	err := os.MkdirAll(dirname, 0755)
	if err != nil {
		return fmt.Errorf("can't make directories for new logfile: %s", err)
	}
	// we use truncate here because this should only get called when we've moved
	// the file ourselves. if someone else creates the file in the meantime,
	// just wipe out the contents.
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("can't open new logfile: %s", err)
	}
	l.file = f
	l.size = 0
	return nil
}

// l.mu must be held by the caller.
// take MaxSize and MaxInterval into consideration.
func (l *Logger) evalCurrentFilename(writeLen int64, forceNewFile bool) string {
	baseFilename := l.currBaseFilename
	if l.currBaseFilename == "" {
		l.currRotationTime = evalCurrRotationTime(l.opts.clock, l.tzOffsetSeconds, l.maxIntervalSeconds)
		baseFilename = genBaseFilename(l.pattern, l.opts.clock, l.currRotationTime)
	} else if l.maxIntervalSeconds > 0 {
		rotationTime := evalCurrRotationTime(l.opts.clock, l.tzOffsetSeconds, l.maxIntervalSeconds)
		if l.currRotationTime != rotationTime {
			l.currRotationTime = rotationTime
			baseFilename = genBaseFilename(l.pattern, l.opts.clock, l.currRotationTime)
		}
	}

	if baseFilename != l.currBaseFilename {
		l.currBaseFilename = baseFilename
		l.currSequence = 0
	} else {
		if forceNewFile || (l.opts.maxSize > 0 && l.size+writeLen > int64(l.opts.maxSize)) {
			l.currSequence++
		}
	}

	genFilename := func(basename string, seq uint) string {
		if seq == 0 {
			return basename
		} else {
			return fmt.Sprintf("%s.%d", basename, seq)
		}
	}

	filename := genFilename(l.currBaseFilename, l.currSequence)
	if forceNewFile {
		// A new file has been requested. Instead of just using the
		// regular strftime pattern, we create a new file name with
		// sequence suffix such as "foo.1", "foo.2", "foo.3", etc
		for {
			if _, err := os.Stat(filename); err != nil {
				// found the first IsNotExist file
				break
			}
			l.currSequence++
			filename = genFilename(l.currBaseFilename, l.currSequence)
		}
	}

	l.currFilename = filename
	return filename
}

// Close implements io.Closer. It closes the current log file and
// the mill goroutine running in background.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.quit <- struct{}{} // close mill goroutine
	l.wg.Wait()
	return l.close()
}

// close closes the file if it is open.
func (l *Logger) close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Rotate forcefully rotates the log files. It will close the existing log file
// and immediately create a new one. This is a helper function for applications
// that want to initiate rotations outside of the normal rotation rules, such
// as in response to SIGHUP. After rotating, this initiates removal of old log
// files according to the configuration.
//
// If the new generated log file name clash because file already exists,
// a sequence suffix of the form ".1", ".2", ".3" and so forth are appended to
// the end of the log file.
func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotate()
}

// rotate closes the current file, opens a new file based on rotation rule,
// and then runs post-rotation processing and removal.
func (l *Logger) rotate() error {
	if err := l.close(); err != nil {
		return err
	}
	filename := l.evalCurrentFilename(0, true)
	if err := l.openNew(filename); err != nil {
		return err
	}
	l.mill()
	return nil
}

// currentFilename returns filename the Logger object is writing to.
func (l *Logger) currentFilename() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.currFilename
}
