// Package logrotate can automatically rotate log files when you write to
// them according to the filename pattern that you can specify.
package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lestrrat-go/strftime"
)

// ensure we always implement io.WriteCloser
var _ io.WriteCloser = (*Logger)(nil)

// Logger is an io.WriteCloser that writes to the specified filename. It
// can get automatically rotated as you write to it.
type Logger struct {
	opts *Options

	pattern         *strftime.Strftime
	globPattern     string
	tzOffsetSeconds int64 // time zone offset in seconds

	mu               sync.RWMutex // guards following
	file             *os.File
	size             int64
	lastRotateTime   int64 // Unix timestamp with location
	currFilename     string
	currBaseFilename string
	currSequence     uint // sequential filename suffix

	// control background mill goroutine
	millCh    chan bool
	startMill sync.Once
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
	if opts.maxInterval < 0 {
		return nil, fmt.Errorf("option MaxInterval cannot be < 0")
	}

	if opts.maxSize < 0 {
		return nil, fmt.Errorf("option MaxSize cannot be < 0")
	}

	_, offset := opts.clock.Now().Zone()

	return &Logger{
		opts:            opts,
		pattern:         filenamePattern,
		globPattern:     globPattern,
		tzOffsetSeconds: int64(offset),
	}, nil
}

// Write satisfies the io.Writer interface. It writes to the
// appropriate file handle that is currently being used.
// If we have reached rotation time, the target file gets
// automatically rotated, and also purged if necessary.
func (l *Logger) Write(p []byte) (n int, err error) {
	// Guard against concurrent writes
	l.mu.Lock()
	defer l.mu.Unlock()

	writeLen := int64(len(p))

	if l.file == nil {
		if err = l.openExistingOrNew(writeLen); err != nil {
			return 0, err
		}
	}

	if l.size+writeLen > int64(l.opts.maxSize) {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	} else {
		if l.currBaseFilename != genBaseFilename(l.pattern, l.opts.clock, l.opts.maxInterval) {
			if err := l.rotate(); err != nil {
				return 0, err
			}
		}
		// TODO: more effective interval compare
		// if l.opts.maxInterval <= 0 {
		// }
		// if l.lastRotateTime == 0 || l.lastRotateTime != l.getLocalNowUnix()/{
		// }
	}

	n, err = l.file.Write(p)
	l.size += int64(n)

	return n, err
}

// openExistingOrNew opens the logfile if it exists and if the current write
// would not put it over MaxSize. If there is no such file or the write would
// put it over the MaxSize, a new file is created.
func (l *Logger) openExistingOrNew(writeLen int64) error {
	l.mill()

	filename := l.evalCurrentFilename(writeLen, false)
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return l.openNew(filename)
	}
	if err != nil {
		return fmt.Errorf("error getting log file info: %s", err)
	}

	if info.Size()+writeLen >= int64(l.opts.maxSize) {
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

// getLocalNowUnix returns the seconds since the Unix epoch in Location.
func (l *Logger) getLocalNowUnix() int64 {
	return l.opts.clock.Now().Unix() + l.tzOffsetSeconds
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
	baseFilename := genBaseFilename(l.pattern, l.opts.clock, l.opts.maxInterval)
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

// millRun runs in a goroutine to manage post-rotation compression and removal
// of old log files.
func (l *Logger) millRun() {
	for range l.millCh {
		// what am I going to do, log this?
		_ = l.millRunOnce()
	}
}

// mill performs post-rotation compression and removal of stale log files,
// starting the mill goroutine if necessary.
func (l *Logger) mill() {
	l.startMill.Do(func() {
		l.millCh = make(chan bool, 1)
		go l.millRun()
	})
	select {
	case l.millCh <- true:
	default:
	}
}

// millRunOnce performs removal of stale log files. Old log
// files are removed, keeping at most MaxBackups files, as long as
// none of them are older than MaxAge.
func (l *Logger) millRunOnce() error {
	if l.opts.maxBackups == 0 && l.opts.maxAge == 0 {
		return nil
	}

	files, err := l.getLogFiles()
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return nil
	}

	if l.opts.linkName != "" {
		// NOTE: file already sorted by ModTime
		latestFilename := files[0].path
		tmpLinkName := latestFilename + ".symlink#"

		// Change how the link name is generated based on where the
		// target location is. if the location is directly underneath
		// the main filename's parent directory, then we create a
		// symlink with a relative path
		linkDest := latestFilename
		linkDir := filepath.Dir(l.opts.linkName)

		baseDir := filepath.Dir(latestFilename)
		if strings.Contains(l.opts.linkName, baseDir) {
			tmp, err := filepath.Rel(linkDir, latestFilename)
			if err != nil {
				return fmt.Errorf("failed to evaluate relative path from %#v to %#v: %v", linkDir, latestFilename, err)
			}
			linkDest = tmp
		}

		if err := os.Symlink(linkDest, tmpLinkName); err != nil {
			return fmt.Errorf("failed to create new symlink: %v", err)
		}

		// the directory where LinkName should be created must exist
		_, err := os.Stat(linkDir)
		if err != nil { // Assume err != nil means the directory doesn't exist
			if err := os.MkdirAll(linkDir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %v", linkDir, err)
			}
		}

		if err := os.Rename(tmpLinkName, l.opts.linkName); err != nil {
			return fmt.Errorf("failed to rename new symlink %s -> %s: %v", tmpLinkName, l.opts.linkName, err)
		}
	}

	// fmt.Printf("files[%d]: %v\n", len(files), files)

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

	// fmt.Printf("removals[%d]: %v\n", len(removals), removals)

	for _, f := range removals {
		os.Remove(f.path)
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

// Close implements io.Closer, and closes the current logfile.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
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

// currentFilename returns filename the Logger object is writing to.
func (l *Logger) currentFilename() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.currFilename
}
