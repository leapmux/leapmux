// Package logging provides structured logging setup with colored
// terminal output (via tint) and runtime-adjustable log levels.
package logging

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

// Level is the global atomic log level. It can be changed at runtime
// (e.g. via the admin API) without restarting the process.
var Level = new(slog.LevelVar) // default: INFO

// Setup initializes the global slog logger. When stderr is a TTY it
// uses tint for colored output; otherwise it falls back to JSON for
// structured log aggregation (Docker, CI).
func Setup() {
	var handler slog.Handler
	if isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		handler = tint.NewHandler(os.Stderr, &tint.Options{
			Level:      Level,
			TimeFormat: time.TimeOnly,
		})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: Level,
		})
	}
	slog.SetDefault(slog.New(handler))
}

// SetLevel changes the global log level.
func SetLevel(l slog.Level) {
	Level.Set(l)
}

// GetLevel returns the current global log level.
func GetLevel() slog.Level {
	return Level.Level()
}

// ParseLevel converts a string like "debug", "info", "warn", "error"
// to the corresponding slog.Level. It is case-insensitive.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(strings.ToUpper(s)))
	return l, err
}
