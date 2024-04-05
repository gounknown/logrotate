module github.com/gounknown/logrotate/examples

go 1.20

require (
	github.com/gounknown/logrotate v0.0.0-20240405083505-0c6d6d14f42e
	go.uber.org/zap v1.27.0
)

require (
	github.com/lestrrat-go/strftime v1.0.6 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	go.uber.org/multierr v1.10.0 // indirect
)

replace github.com/gounknown/logrotate => ../
