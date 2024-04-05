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

	pattern     *strftime.Strftime
	globPattern string

	mu               sync.RWMutex // guards following
	currFile         *os.File
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

	return &Logger{
		opts:        opts,
		pattern:     filenamePattern,
		globPattern: globPattern,
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

	out, err := l.getWriterLocked(false, false)
	if err != nil {
		return 0, fmt.Errorf("failed to get io.Writer: %v", err)
	}

	return out.Write(p)
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
		// NOTE: file already sorted by ModeTime
		latestFilename := files[0].path
		tmpLinkName := genSymlinkFilename(latestFilename)

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

	// the linter tells me to pre allocate this...
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

// getLogFiles returns all log files matched the glob pattern, sorted by ModTime.
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

// l.mu must be held by the caller.
func (l *Logger) getWriterLocked(bailOnRotateFail, rotateSuffixSeq bool) (io.Writer, error) {
	// This filename contains the name of the "NEW" filename
	// to log to, which may be newer than l.currentFilename
	baseFilename := genFilename(l.pattern, l.opts.clock, l.opts.maxInterval)
	filename := baseFilename

	var forceNewFile, sizeRotation bool
	fi, err := os.Stat(l.currFilename)
	if err != nil {
		// TODO: maybe removed by third-party, so need recover automatically
		// tracef(os.Stderr, "%s", err)
	} else {
		if l.opts.maxSize > 0 && int64(l.opts.maxSize) <= fi.Size() {
			forceNewFile = true
			sizeRotation = true
		}
	}

	currSequence := l.currSequence
	if baseFilename != l.currBaseFilename {
		currSequence = 0
	} else {
		if !rotateSuffixSeq && !sizeRotation {
			// nothing to do
			return l.currFile, nil
		}
		forceNewFile = true
		currSequence++
	}

	if forceNewFile {
		// A new file has been requested. Instead of just using the
		// regular strftime pattern, we create a new file name with
		// sequence suffix such as "foo.1", "foo.2", "foo.3", etc
		var name string
		for {
			if currSequence == 0 {
				name = filename
			} else {
				name = fmt.Sprintf("%s.%d", filename, currSequence)
			}
			if _, err := os.Stat(name); err != nil {
				filename = name
				break
			}
			currSequence++
		}
	}

	file, err := createFile(filename)
	if err != nil {
		return nil, err
	}

	if err := l.rotateLocked(filename); err != nil {
		err = fmt.Errorf("failed to rotate: %v", err)
		if bailOnRotateFail {
			// Failure to rotate is a problem, but it's really not a great
			// idea to stop your application just because you couldn't rename
			// your log.
			//
			// We only return this error when explicitly needed (as specified by bailOnRotateFail)
			//
			// However, we *NEED* to close `file` here
			if file != nil { // probably can't happen, but being paranoid
				file.Close()
			}
			return nil, err
		}
		tracef(os.Stderr, "%s", err)
	}

	l.currFile.Close()
	l.currFile = file
	l.currBaseFilename = baseFilename
	l.currFilename = filename
	l.currSequence = currSequence

	return file, nil
}

// l.mu must be held by the caller.
func (l *Logger) rotateLocked(filename string) error {
	lockfn := genLockFilename(filename)
	file, err := os.OpenFile(lockfn, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		// Can't lock, just return
		return err
	}

	var guard cleanupGuard
	guard.fn = func() {
		file.Close()
		os.Remove(lockfn)
	}
	defer guard.Run()

	if l.opts.linkName != "" {
		tmpLinkName := genSymlinkFilename(filename)

		// Change how the link name is generated based on where the
		// target location is. if the location is directly underneath
		// the main filename's parent directory, then we create a
		// symlink with a relative path
		linkDest := filename
		linkDir := filepath.Dir(l.opts.linkName)

		baseDir := filepath.Dir(filename)
		if strings.Contains(l.opts.linkName, baseDir) {
			tmp, err := filepath.Rel(linkDir, filename)
			if err != nil {
				return fmt.Errorf("failed to evaluate relative path from %#v to %#v: %v", linkDir, filename, err)
			}
			linkDest = tmp
		}

		if err := os.Symlink(linkDest, tmpLinkName); err != nil {
			return fmt.Errorf("failed to create new symlink: %v", err)
		}

		// the directory where l.opts.linkName should be created must exist
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

	files, err := filepath.Glob(l.globPattern)
	if err != nil {
		return err
	}

	// fmt.Printf("files[%d]: %v\n", len(files), files)

	// the linter tells me to pre allocate this...
	toRemove := make([]string, 0, len(files))

	if l.opts.maxAge > 0 {
		var remaining []string
		cutoff := l.opts.clock.Now().Add(-1 * l.opts.maxAge)
		for _, path := range files {
			if isLockOrSymlinkFile(path) {
				continue
			}
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			// TODO: use timeformat in filename?
			// Refer: https://github.com/natefinch/lumberjack/blob/v2.0/lumberjack.go#L400
			if fi.ModTime().Before(cutoff) {
				toRemove = append(toRemove, path)
			} else {
				remaining = append(remaining, path)
			}
		}
		files = remaining
	}

	if l.opts.maxBackups > 0 && l.opts.maxBackups < len(files) {
		preserved := make(map[string]bool)
		for _, path := range files {
			if isLockOrSymlinkFile(path) {
				continue
			}
			fl, err := os.Lstat(path)
			if err != nil {
				continue
			}
			if fl.Mode()&os.ModeSymlink == os.ModeSymlink {
				continue
			}
			preserved[path] = true
			if len(preserved) > l.opts.maxBackups {
				// Only remove if we have more than MaxBackups
				toRemove = append(toRemove, path)
			}
		}
	}

	// fmt.Printf("remove[%d]: %v\n", len(toRemove), toRemove)

	if len(toRemove) <= 0 {
		return nil
	}

	guard.Enable()
	go func() {
		// unlink files on a separate goroutine
		for _, path := range toRemove {
			os.Remove(path)
		}
	}()

	return nil
}

// Close implements io.Closer, and closes the current logfile.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.close()
}

// close closes the file if it is open.
func (l *Logger) close() error {
	if l.currFile == nil {
		return nil
	}
	err := l.currFile.Close()
	l.currFile = nil
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
	_, err := l.getWriterLocked(true, true)

	return err
}

// currentFilename returns filename the Logger object is writing to.
func (l *Logger) currentFilename() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.currFilename
}
