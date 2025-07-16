package logger

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/snowmerak/ticketing/lib/adapter"
)

// Logger implementation using zerolog
type Logger struct {
	logger zerolog.Logger
}

// NewLogger creates a new Logger implementation
func NewLogger() *Logger {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	return &Logger{
		logger: logger,
	}
}

// NewLoggerWithLevel creates a new Logger with specified level
func NewLoggerWithLevel(level zerolog.Level) *Logger {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(level)

	return &Logger{
		logger: logger,
	}
}

// Compile-time check to ensure Logger implements adapter.Logger
var _ adapter.Logger = (*Logger)(nil)

// Debug logs a debug message
func (l *Logger) Debug(ctx context.Context, msg string, fields ...interface{}) {
	event := l.logger.Debug()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Info logs an info message
func (l *Logger) Info(ctx context.Context, msg string, fields ...interface{}) {
	event := l.logger.Info()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Warn logs a warning message
func (l *Logger) Warn(ctx context.Context, msg string, fields ...interface{}) {
	event := l.logger.Warn()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Error logs an error message
func (l *Logger) Error(ctx context.Context, msg string, fields ...interface{}) {
	event := l.logger.Error()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Fatal logs a fatal message and exits
func (l *Logger) Fatal(ctx context.Context, msg string, fields ...interface{}) {
	event := l.logger.Fatal()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// WithFields returns a logger with additional fields
func (l *Logger) WithFields(fields map[string]interface{}) adapter.Logger {
	logger := l.logger.With()
	for k, v := range fields {
		logger = logger.Interface(k, v)
	}

	return &Logger{
		logger: logger.Logger(),
	}
}

// addFields adds key-value pairs to the log event
func (l *Logger) addFields(event *zerolog.Event, fields ...interface{}) {
	for i := 0; i < len(fields); i += 2 {
		if i+1 < len(fields) {
			key, ok := fields[i].(string)
			if ok {
				event.Interface(key, fields[i+1])
			}
		}
	}
}

// GetZerolog returns the underlying zerolog logger
func (l *Logger) GetZerolog() zerolog.Logger {
	return l.logger
}
