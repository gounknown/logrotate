// Package logrotate can automatically rotate log files when you write to
// them according to the specified filename pattern and options.
package logrotate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

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

	mu               sync.RWMutex   // guards following
	file             io.WriteCloser // current file handle being written to
	size             int64          // write size of current file
	currRotationTime int64          // Unix timestamp with location
	currFilename     string         // current filename being written to
	currBaseFilename string         // base filename without suffix sequence
	currSequence     uint           // filename suffix sequence

	wg      sync.WaitGroup // counts active background goroutines
	writeCh chan []byte    // buffered chan for write goroutine
	millCh  chan struct{}  // 1-size notification chan for mill goroutine
	quit    chan struct{}  // closed when writeLoop and millLoop should quit

	metrics atomicMetrics

	// mocked out for testing.
	osStat func(name string) (fs.FileInfo, error) // os.Stat
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
	_, offset := opts.clock.Now().Zone()
	l := &Logger{
		opts:               opts,
		pattern:            filenamePattern,
		globPattern:        globPattern,
		maxIntervalSeconds: int64(opts.maxInterval.Seconds()),
		tzOffsetSeconds:    int64(offset),
		millCh:             make(chan struct{}, 1),
		quit:               make(chan struct{}),

		osStat: os.Stat,
	}

	if opts.writeChSize > 0 {
		l.writeCh = make(chan []byte, opts.writeChSize)
		// starting the write goroutine
		l.wg.Add(1)
		go func() {
			l.wg.Done()
			l.writeLoop()
		}()
	}

	// starting the mill goroutine
	l.wg.Add(1)
	go func() {
		l.wg.Done()
		l.millLoop()
	}()

	return l, nil
}

// Write implements io.Writer. If writeChSize <= 0, then it writes to the
// current file directly. Otherwise, it just writes to writeCh, so there is no
// blocking disk I/O operations and would not block unless writeCh is full.
// In the meantime, the writeLoop goroutine will sink the writeCh to files
// asynchronously in background.
//
// Write writes len(b) bytes from b to the File. It returns the number of bytes
// written and an error, if any. Write returns a non-nil error when n != len(b).
//
// NOTE: It's an undefined behavior if you still call Write after Close called.
// Maybe it would sink to files, maybe not, but it won't panic.
func (l *Logger) Write(b []byte) (n int, err error) {
	if l.opts.writeChSize <= 0 {
		return l.write(b)
	}

	// Should check whether the Logger was closed?
	//
	// NOTE: we must do value-copy and then write it to writeCh to avoid the
	// data race problem, as the inputed byte slice "b" is usually reused by
	// the caller.
	//
	// TODO: slice value-copy and GC cost is high, how to optimize? bufio?
	if len(l.writeCh) < l.opts.writeChSize {
		copied := make([]byte, len(b))
		copy(copied, b)
		select {
		case l.writeCh <- copied:
		default:
			l.metrics.Discards.Add(1)
		}
	} else {
		l.metrics.Discards.Add(1)
	}

	return len(b), nil
}

// write writes len(b) bytes to the target file handle that is currently being
// used. It returns the number of bytes written and an error, if any. write
// returns a non-nil error when n != len(b).
//
// If a write would cause the log file to be larger than MaxSize, or we have
// reached a new rotation time (evaluated based on MaxInterval), the target
// file would get automatically rotated, and old log files would also be purged
// if necessary.
func (l *Logger) write(b []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	writeLen := int64(len(b))

	// The os.Stat method cost is: 256 B/op, 2 allocs/op
	_, err = l.osStat(l.currFilename)
	if l.file == nil || errors.Is(err, fs.ErrNotExist) {
		if err = l.openExistingOrNew(writeLen); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	// Factor 1: MaxSize
	if l.opts.maxSize > 0 && l.size+writeLen > int64(l.opts.maxSize) {
		if err = l.rotate(); err != nil {
			return 0, err
		}
	} else {
		// Factor 2: MaxInterval
		if l.maxIntervalSeconds > 0 &&
			l.currRotationTime != evalCurrRotationTime(l.opts.clock, l.tzOffsetSeconds, l.maxIntervalSeconds) {
			if err = l.rotate(); err != nil {
				return 0, err
			}
		}
	}

	n, err = l.file.Write(b)
	l.size += int64(n)

	if err != nil {
		tracef(os.Stderr, "failed to write: %v, try to open existing or new file", err)
		if err1 := l.openExistingOrNew(writeLen); err1 != nil {
			err = errors.Join(err, err1)
			return n, err
		}
	}

	return n, err
}

// writeLoop runs in a goroutine to sink the writeCh until Close is called.
func (l *Logger) writeLoop() {
	for {
		select {
		case <-l.quit:
			// How long to drain on l.writeCh
			drainDu := 10 * time.Millisecond
			if len(l.writeCh) > 100 {
				// give more drain time
				drainDu *= 10
			}
			timer := time.NewTimer(drainDu)
			defer timer.Stop()
			for {
				select {
				case <-timer.C:
					return // quit
				case b := <-l.writeCh:
					_, _ = l.write(b)
				}
			}
		case b := <-l.writeCh:
			// what am I going to do, log this by tracef?
			_, _ = l.write(b)
		}
	}
}

// mill performs post-rotation compression and removal of stale log files.
func (l *Logger) mill() {
	// It's ok to skip if millCh is full.
	select {
	case l.millCh <- struct{}{}:
	default:
	}
}

// millLoop runs in a goroutine to manage post-rotation compression and removal
// of old log files until Close is called.
func (l *Logger) millLoop() {
	for {
		select {
		case <-l.quit:
			// How long to drain on l.millCh
			timer := time.NewTimer(10 * time.Millisecond)
			defer timer.Stop()
			for {
				select {
				case <-timer.C:
					return // quit
				case <-l.millCh:
					_ = l.millRunOnce()
				}
			}
		case <-l.millCh:
			// what am I going to do, log this by tracef?
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

	if l.opts.maxBackups <= 0 && l.opts.maxAge <= 0 {
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

	// try close ahead, since l.file maybe not nil.
	if err := l.close(); err != nil {
		return err
	}

	filename, overMaxSequence := l.evalCurrentFilename(writeLen, false)
	if overMaxSequence {
		return l.openNew(filename)
	}

	info, err := l.osStat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return l.openNew(filename)
	} else if err != nil {
		return fmt.Errorf("get logfile info: %w", err)
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
// take MaxInterval, MaxSequence, and MaxSize into consideration.
func (l *Logger) evalCurrentFilename(writeLen int64, forceNewFile bool) (string, bool) {
	baseFilename := l.currBaseFilename
	if l.currBaseFilename == "" {
		// init base filename if l.currBaseFilename not set
		if l.maxIntervalSeconds > 0 {
			l.currRotationTime = evalCurrRotationTime(l.opts.clock, l.tzOffsetSeconds, l.maxIntervalSeconds)
		} else if l.currRotationTime == 0 {
			// no rotation based on MaxInterval, just set currRotationTime
			// to now only once if not set.
			l.currRotationTime = l.opts.clock.Now().Unix()
		}
		baseFilename = genBaseFilename(l.pattern, l.opts.clock, l.currRotationTime)
	} else if l.maxIntervalSeconds > 0 {
		rotationTime := evalCurrRotationTime(l.opts.clock, l.tzOffsetSeconds, l.maxIntervalSeconds)
		if l.currRotationTime != rotationTime {
			l.currRotationTime = rotationTime
			baseFilename = genBaseFilename(l.pattern, l.opts.clock, l.currRotationTime)
		}
	}
	overMaxSequence := false
	if baseFilename != l.currBaseFilename {
		l.currBaseFilename = baseFilename
		l.currSequence = 0
	} else {
		if forceNewFile || (l.opts.maxSize > 0 && l.size+writeLen > int64(l.opts.maxSize)) {
			overMaxSequence = l.incrCurrSequence()
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
		// sequence suffix such as "foo.1", "foo.2", "foo.3", etc.
		for {
			if overMaxSequence {
				break
			}
			if _, err := l.osStat(filename); err != nil {
				// found the first not existed file
				break
			}
			overMaxSequence = l.incrCurrSequence()
			filename = genFilename(l.currBaseFilename, l.currSequence)
		}
	}

	l.currFilename = filename
	return filename, overMaxSequence
}

func (l *Logger) incrCurrSequence() bool {
	l.currSequence++

	if l.opts.maxSequence > 0 && l.currSequence > uint(l.opts.maxSequence) {
		l.currSequence = uint(l.opts.maxSequence)
		return true
	}
	return false
}

// Close implements io.Closer. It closes the writeLoop and millLoop
// goroutines and the current log file.
func (l *Logger) Close() error {
	close(l.quit) // tell writeLoop and millLoop to quit
	l.wg.Wait()   // and wait until they have quitted

	l.mu.Lock()
	defer l.mu.Unlock()
	// It's ok to not close writeCh and millCh explicitly, because we
	// already closed the writeLoop and millLoop goroutines, so they will
	// be garbage collected. Besides, if you still call Write after Close
	// called, nothing will sink to file.
	//
	// close(l.writeCh)
	// close(l.millCh)
	return l.close()
}

// close closes the file if it is open.
func (l *Logger) close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	l.size = 0
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
	filename, _ := l.evalCurrentFilename(0, true)
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

// Metrics returns metrics of this Logger.
func (l *Logger) Metrics() Metrics {
	return l.metrics.toMetrics()
}
