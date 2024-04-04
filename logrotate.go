// package logrotate allows you to automatically rotate output files when you
// write to them according to the filename pattern that you can specify.
package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lestrrat-go/strftime"
	"github.com/pkg/errors"
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
	globPattern := pattern
	for _, re := range patternConversionRegexps {
		globPattern = re.ReplaceAllString(globPattern, "*")
	}

	filenamePattern, err := strftime.New(pattern)
	if err != nil {
		return nil, errors.Wrap(err, `invalid strftime pattern`)
	}

	opts := parseOptions(options...)

	if opts.maxAge < 0 {
		return nil, errors.New("option MaxAge cannot be < 0")
	}
	if opts.maxInterval < 0 {
		return nil, errors.New("option MaxInterval cannot be < 0")
	}

	if opts.maxSize < 0 {
		return nil, errors.New("option MaxSize cannot be < 0")
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
func (r *Logger) Write(p []byte) (n int, err error) {
	// Guard against concurrent writes
	r.mu.Lock()
	defer r.mu.Unlock()

	out, err := r.getWriterLocked(false, false)
	if err != nil {
		return 0, errors.Wrap(err, `failed to acquite target io.Writer`)
	}

	return out.Write(p)
}

// r.mu must be held by the caller.
func (r *Logger) getWriterLocked(bailOnRotateFail, rotateSuffixSeq bool) (io.Writer, error) {
	suffixSeq := r.suffixSeq

	// This filename contains the name of the "NEW" filename
	// to log to, which may be newer than r.currentFilename
	baseFilename := genFilename(r.pattern, r.opts.clock, r.opts.maxInterval)
	filename := baseFilename
	var forceNewFile bool

	fi, err := os.Stat(r.currFilename)
	sizeRotation := false
	if err == nil && r.opts.maxSize > 0 && int64(r.opts.maxSize) <= fi.Size() {
		forceNewFile = true
		sizeRotation = true
	}

	if baseFilename != r.currBaseFilename {
		suffixSeq = 0
	} else {
		if !rotateSuffixSeq && !sizeRotation {
			// nothing to do
			return r.outFile, nil
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
		return nil, errors.Wrapf(err, `failed to create a new file %v`, filename)
	}

	if err := r.rotateLocked(filename); err != nil {
		err = errors.Wrap(err, "failed to rotate")
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

	r.outFile.Close()
	r.outFile = fh
	r.currBaseFilename = baseFilename
	r.currFilename = filename
	r.suffixSeq = suffixSeq

	return fh, nil
}

// r.mu must be held by the caller.
func (r *Logger) rotateLocked(filename string) error {
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

	if r.opts.linkName != "" {
		tmpLinkName := filename + `_symlink`

		// Change how the link name is generated based on where the
		// target location is. if the location is directly underneath
		// the main filename's parent directory, then we create a
		// symlink with a relative path
		linkDest := filename
		linkDir := filepath.Dir(r.opts.linkName)

		baseDir := filepath.Dir(filename)
		if strings.Contains(r.opts.linkName, baseDir) {
			tmp, err := filepath.Rel(linkDir, filename)
			if err != nil {
				return errors.Wrapf(err, `failed to evaluate relative path from %#v to %#v`, baseDir, r.opts.linkName)
			}

			linkDest = tmp
		}

		if err := os.Symlink(linkDest, tmpLinkName); err != nil {
			return errors.Wrap(err, `failed to create new symlink`)
		}

		// the directory where r.opts.linkName should be created must exist
		_, err := os.Stat(linkDir)
		if err != nil { // Assume err != nil means the directory doesn't exist
			if err := os.MkdirAll(linkDir, 0755); err != nil {
				return errors.Wrapf(err, `failed to create directory %s`, linkDir)
			}
		}

		if err := os.Rename(tmpLinkName, r.opts.linkName); err != nil {
			return errors.Wrap(err, `failed to rename new symlink`)
		}
	}

	files, err := filepath.Glob(r.globPattern)
	if err != nil {
		return err
	}

	// fmt.Printf("files[%d]: %v\n", len(files), files)

	// the linter tells me to pre allocate this...
	toRemove := make([]string, 0, len(files))

	if r.opts.maxAge > 0 {
		var remaining []string
		cutoff := r.opts.clock.Now().Add(-1 * r.opts.maxAge)
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

	if r.opts.maxBackups > 0 && r.opts.maxBackups < len(files) {
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
			if len(preserved) > r.opts.maxBackups {
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
func (r *Logger) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.outFile == nil {
		return nil
	}

	r.outFile.Close()
	r.outFile = nil

	return nil
}

// Rotate forcefully rotates the log files. If the generated file name
// clash because file already exists, a numeric suffix of the form
// ".1", ".2", ".3" and so forth are appended to the end of the log file.
//
// This method can be used in conjunction with a signal handler so to
// emulate servers that generate new log files when they receive a
// SIGHUP.
func (r *Logger) Rotate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.getWriterLocked(true, true)

	return err
}

// CurrentFileName returns the current file name that
// the Logger object is writing to.
func (r *Logger) CurrentFileName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.currFilename
}
