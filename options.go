package logrotate

import (
	"time"
)

// Options is supplied as the optional arguments for New.
type Options struct {
	clock       Clock         // used to determine the current time
	symlink     string        // linked to the current file
	maxInterval time.Duration // max interval between file rotation
	maxSequence int           // max count of log files in the same interval
	maxSize     int           // max size of log file before rotation
	maxAge      time.Duration // max age to retain old log files
	maxBackups  int           // max number of old log files to retain
	writeChSize int           // buffered write channel size
}

// Option is the functional option type.
type Option func(*Options)

func newDefaultOptions() *Options {
	return &Options{
		clock:       DefaultClock,
		symlink:     "",                // no symlink
		maxInterval: 24 * time.Hour,    // 24 hours
		maxSize:     100 * 1024 * 1024, // 100M
		maxAge:      0,                 // retain all old log files
		maxBackups:  0,                 // retain all old log files
		writeChSize: 0,                 // do not use buffered write.
	}
}

func parseOptions(setters ...Option) *Options {
	// default Options
	opts := newDefaultOptions()
	for _, setter := range setters {
		setter(opts)
	}
	return opts
}

// WithClock specifies the clock used by Logger to determine the current
// time. It defaults to the system clock with time.Now.
func WithClock(clock Clock) Option {
	return func(opts *Options) {
		opts.clock = clock
	}
}

// WithSymlink sets the symbolic link name that gets linked to
// the current filename being used.
//
// Default: ""
func WithSymlink(name string) Option {
	return func(opts *Options) {
		opts.symlink = name
	}
}

// WithMaxInterval sets the maximum interval between file rotation.
// In particular, the minimal interval unit is in time.Second level.
//
// Default: 24 hours
func WithMaxInterval(d time.Duration) Option {
	return func(opts *Options) {
		opts.maxInterval = d
	}
}

// WithMaxSequence controls the max count of rotated log files in the same
// interval. If over max sequence limit, the logger will clear content of
// the log file with max sequence suffix, and then write to it.
//
// If MaxSequence <= 0, that means no limit of rotated log files in the
// same interval.
//
// Default: 0
func WithMaxSequence(n int) Option {
	return func(opts *Options) {
		opts.maxSequence = n
	}
}

// WithMaxSize sets the maximum size of log file before it gets
// rotated. If MaxSize <= 0, that means not rotate log file based
// on size.
//
// Default: 100 MiB
func WithMaxSize(s int) Option {
	return func(opts *Options) {
		opts.maxSize = s
	}
}

// WithMaxAge sets the max age to retain old log files based on the
// timestamp encoded in their filename. If MaxAge <= 0, that means
// not remove old log files based on age.
//
// Default: 0
func WithMaxAge(d time.Duration) Option {
	return func(opts *Options) {
		opts.maxAge = d
	}
}

// WithMaxBackups sets the maximum number of old log files to retain.
// If MaxBackups <= 0, that means retain all old log files (though
// MaxAge may still cause them to be removed.)
//
// Default: 0
func WithMaxBackups(n int) Option {
	return func(opts *Options) {
		opts.maxBackups = n
	}
}

// WithWriteChan sets the buffered write channel size.
//
// If write chan size <= 0, it will write to the current file directly.
//
// If write chan size > 0, the logger just writes to write chan and return,
// and it's the write loop goroutine's responsibility to sink the write channel
// to files asynchronously in background. So there is no blocking disk I/O
// operations, and write would not block even if write channel is full as it will
// auto discard log lines.
//
// Default: 0
func WithWriteChan(size int) Option {
	return func(opts *Options) {
		opts.writeChSize = size
	}
}
