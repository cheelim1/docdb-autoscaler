package logger

import (
	"log/slog"
	"os"
)

// NewLogger initializes and returns a new structured logger.
func NewLogger() *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo, // Set the desired log level
	})
	logger := slog.New(handler)
	return logger
}
