package logrotate

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/lestrrat-go/strftime"
)

func Test_tracef(t *testing.T) {
	type args struct {
		format string
		args   []any
	}
	tests := []struct {
		name    string
		args    args
		want    int
		wantW   string
		wantErr bool
	}{
		{
			name: "case-1",
			args: args{
				format: "test %d %s",
				args:   []any{1, "hello"},
			},
			want:    57,
			wantW:   "util_test.go:39 logrotate.Test_tracef.func1 test 1 hello\n",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &bytes.Buffer{}
			got, err := tracef(w, tt.args.format, tt.args.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("tracef() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("tracef() = %v, want %v", got, tt.want)
			}
			if gotW := w.String(); gotW != tt.wantW {
				t.Errorf("tracef() = %v, want %v", gotW, tt.wantW)
			}
		})
	}
}

func Test_genBaseFilename(t *testing.T) {
	// filename pattern
	pattern, err := strftime.New("/path/to/%Y/%m/%d/%H/%M/%S")
	if err != nil {
		t.Fatalf("strftime.New failed: %v", err)
	}
	// Mock time
	ts := []time.Time{
		time.Now(),
		time.Now().Add(time.Hour + time.Minute + time.Second),
		(time.Time{}).Add(24 * time.Hour),
	}
	genExpectedName := func(t time.Time) string {
		return fmt.Sprintf("/path/to/%04d/%02d/%02d/%02d/%02d/%02d",
			t.Year(),
			t.Month(),
			t.Day(),
			t.Hour(),
			t.Minute(),
			t.Second(),
		)
	}
	genIntervalTime := func(clock clockwork.FakeClock) int64 {
		_, offset := clock.Now().Zone()
		now := clock.Now().Unix() + int64(offset)
		// tracef(os.Stderr, "now: %v", now)
		interval := time.Second
		t := now - (now % int64(interval.Seconds()))
		// tracef(os.Stderr, "genIntervalTime: %v", t)
		return t
	}
	type args struct {
		pattern      *strftime.Strftime
		clock        interface{ Now() time.Time }
		intervalTime int64
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "case-0",
			args: args{
				pattern:      pattern,
				clock:        clockwork.NewFakeClockAt(ts[0]),
				intervalTime: genIntervalTime(clockwork.NewFakeClockAt(ts[0])),
			},
			want: genExpectedName(ts[0]),
		},
		{
			name: "case-1",
			args: args{
				pattern:      pattern,
				clock:        clockwork.NewFakeClockAt(ts[1]),
				intervalTime: genIntervalTime(clockwork.NewFakeClockAt(ts[1])),
			},
			want: genExpectedName(ts[1]),
		},
		{
			name: "case-2",
			args: args{
				pattern:      pattern,
				clock:        clockwork.NewFakeClockAt(ts[2]),
				intervalTime: genIntervalTime(clockwork.NewFakeClockAt(ts[2])),
			},
			want: genExpectedName(ts[2]),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := genBaseFilename(tt.args.pattern, tt.args.clock, tt.args.intervalTime); got != tt.want {
				t.Errorf("genFilename() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_genFilename2(t *testing.T) {
	// filename pattern
	pattern, err := strftime.New("/path/to/%Y/%m/%d")
	if err != nil {
		t.Fatalf("strftime.New failed: %v", err)
	}
	// Mock time
	ts := []time.Time{
		{},
		(time.Time{}).Add(24 * time.Hour),
	}
	genExpectedName := func(t time.Time) string {
		return fmt.Sprintf("/path/to/%04d/%02d/%02d",
			t.Year(),
			t.Month(),
			t.Day(),
		)
	}
	type args struct {
		pattern  *strftime.Strftime
		clock    interface{ Now() time.Time }
		interval time.Duration
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "case-0",
			args: args{
				pattern:  pattern,
				clock:    clockwork.NewFakeClockAt(ts[0]),
				interval: 24 * time.Hour,
			},
			want: genExpectedName(ts[0]),
		},
		{
			name: "case-1",
			args: args{
				pattern:  pattern,
				clock:    clockwork.NewFakeClockAt(ts[1]),
				interval: 24 * time.Hour,
			},
			want: genExpectedName(ts[1]),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := genBaseFilename2(tt.args.pattern, tt.args.clock, tt.args.interval); got != tt.want {
				t.Errorf("genFilename() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseGlobPattern(t *testing.T) {
	type args struct {
		pattern string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test-strftime",
			args: args{
				pattern: "test_%Y%m%d%H%M%S",
			},
			want: "test_*",
		},
		{
			name: "format-strftime",
			args: args{
				pattern: "test_%Y-%m-%d %H:%M:%S",
			},
			want: "test_*-*-* *:*:*",
		},
		{
			name: "all-strftime",
			args: args{
				pattern: "%Y%m%d%H%M%S",
			},
			want: "*",
		},
		{
			name: "with-one-*",
			args: args{
				pattern: "test_*%Y%m%d%H%M%S",
			},
			want: "test_*",
		},
		{
			name: "with-mutiple-*",
			args: args{
				pattern: "test_***%Y%m%d%H%M%S**",
			},
			want: "test_*",
		},
		{
			name: "escape-ok-%%",
			args: args{
				pattern: "test_%%%Y%m%d%H%M%S",
			},
			want: "test_*",
		},
		{
			name: "escape-miss-%",
			args: args{
				pattern: "test_%%Y%m%d%H%M%S",
			},
			want: "test_*Y*",
		},
		{
			name: "with-file-ext",
			args: args{
				pattern: "test_%Y%m%d%H%M%S.log",
			},
			want: "test_*.log",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseGlobPattern(tt.args.pattern); got != tt.want {
				t.Errorf("parseGlobPattern() = %v, want %v", got, tt.want)
			}
		})
	}
}
