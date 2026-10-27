package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/ml8s/agent-runtime/internal/platform/config"
)

func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	if strings.ToLower(cfg.LogFormat) == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, options))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, options))
}
