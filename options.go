package logrotate

import (
	"time"
)

// Options is supplied as the optional arguments for New.
type Options struct {
	clock       Clock
	symlink     string
	maxInterval time.Duration
	maxSize     int
	maxAge      time.Duration
	maxBackups  int
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

// WithMaxSize sets the maximum size of log file before it gets
// rotated. 0 means that do not rotate log file based on size.
//
// Default: 100 megabytes
func WithMaxSize(s int) Option {
	return func(opts *Options) {
		opts.maxSize = s
	}
}

// WithMaxAge sets the max age to retain old log files based on the
// timestamp encoded in their filename. 0 means not to remove
// old log files based on age.
//
// Default: 0
func WithMaxAge(d time.Duration) Option {
	return func(opts *Options) {
		opts.maxAge = d
	}
}

// WithMaxBackups sets the maximum number of old log files to retain.
// 0 means that retain all old log files (though MaxAge may still cause
// them to get deleted.)
//
// Default: 0
func WithMaxBackups(n int) Option {
	return func(opts *Options) {
		opts.maxBackups = n
	}
}
