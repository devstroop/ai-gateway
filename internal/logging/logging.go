package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func New(level string, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stdout
	}
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
