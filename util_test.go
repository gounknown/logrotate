package logrotate

import (
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/lestrrat-go/strftime"
)

func Test_genFilename(t *testing.T) {
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
		pattern      *strftime.Strftime
		clock        interface{ Now() time.Time }
		rotationTime time.Duration
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
				rotationTime: 24 * time.Hour,
			},
			want: genExpectedName(ts[0]),
		},
		{
			name: "case-1",
			args: args{
				pattern:      pattern,
				clock:        clockwork.NewFakeClockAt(ts[1]),
				rotationTime: 24 * time.Hour,
			},
			want: genExpectedName(ts[1]),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := genFilename(tt.args.pattern, tt.args.clock, tt.args.rotationTime); got != tt.want {
				t.Errorf("genFilename() = %v, want %v", got, tt.want)
			}
		})
	}
}
