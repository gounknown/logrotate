# logrotate

A powerful log rotation package for Go.

## Examples

### Use with Zap

```go
func main() {
  // logrotate is already safe for concurrent use, so we don't need to
  // lock it.
  r, err := logrotate.New(
    "/path/to/access_log.%Y%m%d%H%M",
    logrotate.WithLinkName("/path/to/access_log"),
    logrotate.WithMaxAge(24 * time.Hour),
    logrotate.WithMaxInterval(time.Hour),
  )
  if err != nil {
    log.Printf("failed to create logrotate: %s", err)
    return
  }

  w := zapcore.AddSync(r)
  core := zapcore.NewCore(
    zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
    w,
    zap.InfoLevel,
  )
  logger := zap.New(core)
}
```

### Use with stdlib log

```go
func main() {
  r, _ := logrotate.New("/path/to/access_log.%Y%m%d%H%M")

  log.SetOutput(r)

  /* elsewhere ... */
  log.Printf("Hello, World!")
}
```

## Options

### Pattern (Required)

The pattern used to generate actual log file names. You should use patterns
using the strftime (3) format. For example:

```go
  logrotate.New("/var/log/myapp/log.%Y%m%d")
```

### Clock (default: logrotate.DefaultClock)

You may specify an object that implements the roatatelogs.Clock interface.
When this option is supplied, it's used to determine the current time to
base all of the calculations on. For example, if you want to base your
calculations in UTC, you may create a UTC clock:

```go
type UTCClock struct{}

func (UTCClock) Now() time.Time {
	return time.Now().UTC()
}

logrotate.New(
  "/var/log/myapp/log.%Y%m%d",
  logrotate.WithClock(UTCClock),
)
```

### LinkName (default: "")

Path where a symlink for the actual log file is placed. This allows you to
always check at the same location for log files even if the logs were rotated

```go
  logrotate.New(
    "/var/log/myapp/log.%Y%m%d",
    logrotate.WithLinkName("/var/log/myapp/current"),
  )
```

```
  // Else where
  $ tail -f /var/log/myapp/current
```

Links that share the same parent directory with the main log path will get a
special treatment: namely, linked paths will be *RELATIVE* to the main log file.

| Main log file name  | Link name           | Linked path           |
| ------------------- | ------------------- | --------------------- |
| /path/to/log.%Y%m%d | /path/to/log        | log.YYYYMMDD          |
| /path/to/log.%Y%m%d | /path/to/nested/log | ../log.YYYYMMDD       |
| /path/to/log.%Y%m%d | /foo/bar/baz/log    | /path/to/log.YYYYMMDD |

If not provided, no link will be written.

### MaxInterval (default: 24 hours)

Interval between file rotation. By default logs are rotated every 24 hours.

Note: Remember to use time.Duration values.

```go
  // Rotate every hour
  logrotate.New(
    "/var/log/myapp/log.%Y%m%d",
    logrotate.WithMaxInterval(time.Hour),
  )
```

### MaxSize(default: 100 megabytes)

MaxSize is the maximum size in megabytes of the log file before it gets
rotated. It defaults to 100 megabytes.

```go
  // Rotate every 10M
  logrotate.New(
    "/var/log/myapp/log.%Y%m%d",
    logrotate.WithMaxSize(10*1024*1024),
  )
```

### MaxAge (default: 0)

Retain old log files based on the timestamp encoded in their filename.
The default is not to remove old log files based on age.

Note: Remember to use time.Duration values.

```go
  // Remove logs older than 7 days
  logrotate.New(
    "/var/log/myapp/log.%Y%m%d",
    logrotate.WithMaxAge(7*24*time.Hour),
  )
```

### MaxBackups (default: 0)

The maximum number of old log files to retain. The default
is to retain all old log files (though MaxAge may still cause them to get
deleted.)

```go
  // Remove logs except latest 7 files
  logrotate.New(
    "/var/log/myapp/log.%Y%m%d",
    logrotate.WithMaxBackups(7),
  )
```
