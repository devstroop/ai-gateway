package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/devstroop/ai-gateway-go/internal/auth"
	"github.com/devstroop/ai-gateway-go/internal/config"
	"github.com/devstroop/ai-gateway-go/internal/handlers"
	"github.com/devstroop/ai-gateway-go/internal/health"
	"github.com/devstroop/ai-gateway-go/internal/middleware"
)

type Server struct {
	cfg    config.Config
	logger *slog.Logger
	http   *http.Server
}

func New(cfg config.Config, logger *slog.Logger) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", health.Health)
	mux.HandleFunc("/ready", health.ReadyHandler(nil))

	chatHandler := handlers.NewChatHandler(cfg, logger)
	mux.HandleFunc("/v1/chat/completions", chatHandler.ServeHTTP)

	modelsHandler := handlers.NewModelsHandler(cfg, logger)
	mux.HandleFunc("/v1/models", modelsHandler.ServeHTTP)
	mux.HandleFunc("/v1/auth/whoami", handlers.Whoami)

	messagesHandler := handlers.NewPassthroughHandler(cfg, logger, "/messages", []config.ApiType{config.ApiTypeMessages})
	mux.HandleFunc("/v1/messages", messagesHandler.ServeHTTP)

	responsesHandler := handlers.NewPassthroughHandler(cfg, logger, "/responses", []config.ApiType{config.ApiTypeResponses})
	mux.HandleFunc("/v1/responses", responsesHandler.ServeHTTP)

	// Middleware order (outermost -> innermost): Recover -> RequestID -> Logging -> mux.
	// Recover is outermost to catch panics from RequestID; Logging folds its own
	// recovery for the inner chain. RequestID must wrap Logging so the request
	// context carries the id. Do not reorder.
	handler := auth.Middleware(cfg)(mux)
	handler = middleware.Logging(logger)(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.Recover(logger)(handler)

	s := &Server{cfg: cfg, logger: logger}
	s.http = &http.Server{
		Addr:              net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Timeout(),
		WriteTimeout:      0, // disabled: allow long/streaming responses
		IdleTimeout:       120 * time.Second,
	}
	return s
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("server starting", "addr", s.http.Addr)
		errCh <- s.http.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
