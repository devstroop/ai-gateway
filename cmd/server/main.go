package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/devstroop/ai-gateway/internal/config"
	"github.com/devstroop/ai-gateway/internal/logging"
	"github.com/devstroop/ai-gateway/internal/server"
)

func main() {
	cfgPath := os.Getenv("CONFIG_PATH")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.LogLevel, nil)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, logger)
	if err := srv.ListenAndServe(ctx); err != nil {
		logger.Error("server stopped", "error", err)
		stop()
		os.Exit(1)
	}
	logger.Info("server stopped gracefully")
}
