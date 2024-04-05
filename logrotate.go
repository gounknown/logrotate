// Package logrotate can automatically rotate log files when you write to
// them according to the filename pattern that you can specify.
package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	outFile          *os.File
	currFilename     string
	currBaseFilename string
	suffixSeq        uint // a numeric filename suffix sequence
}

// New creates a new Logger object with specified filename pattern.
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

// l.mu must be held by the caller.
func (l *Logger) getWriterLocked(bailOnRotateFail, rotateSuffixSeq bool) (io.Writer, error) {
	suffixSeq := l.suffixSeq

	// This filename contains the name of the "NEW" filename
	// to log to, which may be newer than l.currentFilename
	baseFilename := genFilename(l.pattern, l.opts.clock, l.opts.maxInterval)
	filename := baseFilename
	var forceNewFile bool

	fi, err := os.Stat(l.currFilename)
	sizeRotation := false
	if err == nil && l.opts.maxSize > 0 && int64(l.opts.maxSize) <= fi.Size() {
		forceNewFile = true
		sizeRotation = true
	}

	if baseFilename != l.currBaseFilename {
		suffixSeq = 0
	} else {
		if !rotateSuffixSeq && !sizeRotation {
			// nothing to do
			return l.outFile, nil
		}
		forceNewFile = true
		suffixSeq++
	}
	if forceNewFile {
		// A new file has been requested. Instead of just using the
		// regular strftime pattern, we create a new file name using
		// generational names such as "foo.1", "foo.2", "foo.3", etc
		var name string
		for {
			if suffixSeq == 0 {
				name = filename
			} else {
				name = fmt.Sprintf("%s.%d", filename, suffixSeq)
			}
			if _, err := os.Stat(name); err != nil {
				filename = name
				break
			}
			suffixSeq++
		}
	}

	fh, err := createFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new file: %v", err)
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
			// However, we *NEED* to close `fh` here
			if fh != nil { // probably can't happen, but being paranoid
				fh.Close()
			}
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
	}

	l.outFile.Close()
	l.outFile = fh
	l.currBaseFilename = baseFilename
	l.currFilename = filename
	l.suffixSeq = suffixSeq

	return fh, nil
}

// l.mu must be held by the caller.
func (l *Logger) rotateLocked(filename string) error {
	lockfn := filename + `_lock`
	fh, err := os.OpenFile(lockfn, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		// Can't lock, just return
		return err
	}

	var guard cleanupGuard
	guard.fn = func() {
		fh.Close()
		os.Remove(lockfn)
	}
	defer guard.Run()

	if l.opts.linkName != "" {
		tmpLinkName := filename + `_symlink`

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
			// Ignore lock files
			if strings.HasSuffix(path, "_lock") || strings.HasSuffix(path, "_symlink") {
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
			// Ignore lock files
			if strings.HasSuffix(path, "_lock") || strings.HasSuffix(path, "_symlink") {
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

// Close satisfies the io.Closer interface. You must
// call this method if you performed any writes to
// the object.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.outFile == nil {
		return nil
	}

	l.outFile.Close()
	l.outFile = nil

	return nil
}

// Rotate forcefully rotates the log files. If the generated file name
// clash because file already exists, a numeric suffix of the form
// ".1", ".2", ".3" and so forth are appended to the end of the log file.
//
// This method can be used in conjunction with a signal handler so to
// emulate servers that generate new log files when they receive a
// SIGHUP.
func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.getWriterLocked(true, true)

	return err
}

// CurrentFileName returns the current file name that
// the Logger object is writing to.
func (l *Logger) CurrentFileName() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.currFilename
}
