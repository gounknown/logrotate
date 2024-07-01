# logrotate
[![GoDoc](https://godoc.org/github.com/carlmjohnson/requests?status.svg)](https://pkg.go.dev/github.com/gounknown/logrotate) 
[![Go Report Card](https://goreportcard.com/badge/github.com/carlmjohnson/requests)](https://goreportcard.com/report/github.com/gounknown/logrotate)
[![Coverage Status](https://codecov.io/gh/gounknown/logrotate/branch/master/graph/badge.svg)](https://codecov.io/gh/gounknown/logrotate) 
[![License](https://img.shields.io/github/license/gounknown/logrotate?style=flat-square)](https://opensource.org/licenses/MIT)

A powerful log rotation package for Go.

## Examples

### Use with stdlib log

> See demo: [ Use with stdlib log](./examples/stdlog/main.go)

```go
func main() {
    // logrotate is safe for concurrent use.
    l, _ := logrotate.New("/path/to/log.%Y%m%d")
    log.SetOutput(l)
    log.Printf("Hello, World!")
}
```

### Use with Zap

> See demo: [Use with Zap](./examples/zap/main.go)

```go
func main() {
    l, _ := logrotate.New(
        "/path/to/log.%Y%m%d%H",
        logrotate.WithSymlink("/path/to/log"),  // symlink to current logfile
        logrotate.WithMaxAge(30*24*time.Hour),  // remove logs older than 30 days
        logrotate.WithMaxInterval(time.Hour),   // rotate every hour
    )
    w := zapcore.AddSync(l)
    core := zapcore.NewCore(
        zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
        w,
        zap.InfoLevel,
    )
    logger := zap.New(core)
    logger.Info("Hello, World!")
}
```

## Options

### Pattern (Required)

> See [strftime: supported conversion specifications](https://github.com/lestrrat-go/strftime?tab=readme-ov-file#supported-conversion-specifications)

The pattern used to generate actual log file names. You should use the
[strftime (3) - format date and time](https://man7.org/linux/man-pages/man3/strftime.3.html) format.
For example:

```go
// YYYY-mm-dd (e.g.: 2024-04-04)
logrotate.New("/path/to/log.%Y-%m-%d")
// YY-mm-dd HH:MM:SS (e.g.: 2024-04-04 10:01:49)
logrotate.New("/path/to/log.%Y-%m-%d %H:%M:%S")
```

### Clock (default: logrotate.DefaultClock)

You may specify an object that implements the `logrotate.Clock` interface.
When this option is supplied, it's used to determine the current time to
base all of the calculations on. For example, if you want to base your
calculations in UTC, you may create a UTC clock:

```go
type UTCClock struct{}

func (UTCClock) Now() time.Time {
    return time.Now().UTC()
}

logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithClock(UTCClock),
)
```

### Symlink (default: "")

You can set a symlink for the current log file being used. This allows you to
always check at the same location for current log file even if the logs were
rotated.

```go
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithSymlink("/path/to/current"),
)
```

```bash
# Check current log file
$ tail -f /path/to/current
```

The symlink that share the same parent directory with the main log path will get
a special treatment: linked paths will be *RELATIVE* to the main log file.

| Main log file pattern | Symlink path        | Linked path           |
| --------------------- | ------------------- | --------------------- |
| /path/to/log.%Y%m%d   | /path/to/log        | log.YYYYMMDD          |
| /path/to/log.%Y%m%d   | /path/to/nested/log | ../log.YYYYMMDD       |
| /path/to/log.%Y%m%d   | /foo/bar/baz/log    | /path/to/log.YYYYMMDD |

If not provided, no link will be written.

### MaxInterval (default: 24 hours)

Interval between file rotation. By default logs are rotated every 24 hours.
In particular, the minimal interval unit is in `time.Second` level.

Note: Remember to use `time.Duration` values.

```go
// Rotate every hour
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithMaxInterval(time.Hour),
)
```


### MaxSequence (default: 0)

MaxSequence controls the max count of rotated log files in the same
interval. If over max sequence limit, the logger will clear content of
the log file with max sequence suffix, and then write to it.

If MaxSequence <= 0, that means no  limit of rotated log files in the
same interval.

```go
// limit max 100 rotated files in the same interval
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.MaxSequence(100),
)
```

### MaxSize (default: 100 MiB)

MaxSize is the maximum size in MiB (megabytes) of the log file before it gets
rotated. It defaults to 100 MiB.

```go
// Rotate every 10 MiB
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithMaxSize(10*1024*1024),
)
```

### MaxAge (default: 0)

Retain old log files based on the timestamp encoded in their filename.
If MaxAge <= 0, that means not remove old log files based on age.

Note: Remember to use `time.Duration` values.

```go
// Remove logs older than 7 days
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithMaxAge(7*24*time.Hour),
)
```

### MaxBackups (default: 0)

The maximum number of old log files to retain. If MaxBackups <= 0, that means 
retain all old log files (though MaxAge may still cause them to be removed.)

```go
// Remove logs except latest 7 files
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithMaxBackups(7),
)
```

### WriteChan (default: 0)

WithWriteChan sets the buffered write channel size.

If write chan size <= 0, it will write to the current file directly.

If write chan size > 0, the logger just writes to write chan and return,
and it's the write loop goroutine's responsibility to sink the write channel
to files asynchronously in background. So there is no blocking disk I/O
operations, and write would not block even if write channel is full as it will
auto discard log lines.

```go
// Use buffered write and set channel size to 100
logrotate.New(
    "/path/to/log.%Y%m%d",
    logrotate.WithWriteChan(100),
)
```
