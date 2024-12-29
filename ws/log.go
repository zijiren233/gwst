package ws

type Logger interface {
	Info(...any)
	Infof(string, ...any)
	Warn(...any)
	Warnf(string, ...any)
	Error(...any)
	Errorf(string, ...any)
}

var _ Logger = (*safeLogger)(nil)

type safeLogger struct {
	logger Logger
}

func newSafeLogger(logger Logger) *safeLogger {
	return &safeLogger{logger: logger}
}

func (sl *safeLogger) Info(v ...any) {
	if sl.logger != nil {
		sl.logger.Info(v...)
	}
}

func (sl *safeLogger) Infof(format string, v ...any) {
	if sl.logger != nil {
		sl.logger.Infof(format, v...)
	}
}

func (sl *safeLogger) Warn(v ...any) {
	if sl.logger != nil {
		sl.logger.Warn(v...)
	}
}

func (sl *safeLogger) Warnf(format string, v ...any) {
	if sl.logger != nil {
		sl.logger.Warnf(format, v...)
	}
}

func (sl *safeLogger) Error(v ...any) {
	if sl.logger != nil {
		sl.logger.Error(v...)
	}
}

func (sl *safeLogger) Errorf(format string, v ...any) {
	if sl.logger != nil {
		sl.logger.Errorf(format, v...)
	}
}
