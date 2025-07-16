package adapter

import (
	"context"
)

// Logger defines the interface for logging operations
type Logger interface {
	// Debug logs a debug message
	Debug(ctx context.Context, msg string, fields ...interface{})

	// Info logs an info message
	Info(ctx context.Context, msg string, fields ...interface{})

	// Warn logs a warning message
	Warn(ctx context.Context, msg string, fields ...interface{})

	// Error logs an error message
	Error(ctx context.Context, msg string, fields ...interface{})

	// Fatal logs a fatal message and exits
	Fatal(ctx context.Context, msg string, fields ...interface{})

	// WithFields returns a logger with additional fields
	WithFields(fields map[string]interface{}) Logger
}
