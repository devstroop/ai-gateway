package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/devstroop/ai-gateway/internal/admin"
	"github.com/devstroop/ai-gateway/internal/auth"
	"github.com/devstroop/ai-gateway/internal/config"
	"github.com/devstroop/ai-gateway/internal/handlers"
	"github.com/devstroop/ai-gateway/internal/health"
	"github.com/devstroop/ai-gateway/internal/middleware"
	"github.com/devstroop/ai-gateway/internal/storage"
)

type Server struct {
	cfg    config.Config
	logger *slog.Logger
	db     *storage.DB
	http   *http.Server
}

func New(cfg config.Config, logger *slog.Logger) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", health.Health)
	mux.HandleFunc("/ready", health.ReadyHandler(nil))

	hasChatProvider := false
	for _, p := range cfg.EffectiveProviders() {
		if p.APIType == "" || p.APIType == config.ApiTypeChatCompletions {
			hasChatProvider = true
			break
		}
	}
	var chatHandler http.Handler
	if hasChatProvider {
		chatHandler = handlers.NewChatHandler(cfg, logger)
	} else {
		chatHandler = handlers.NewChatBridgeHandler(cfg, logger)
	}
	mux.HandleFunc("/v1/chat/completions", chatHandler.ServeHTTP)

	modelsHandler := handlers.NewModelsHandler(cfg, logger)
	mux.HandleFunc("/v1/models", modelsHandler.ServeHTTP)

	embeddingsHandler := handlers.NewEmbeddingsHandler(cfg, logger)
	mux.HandleFunc("/v1/embeddings", embeddingsHandler.ServeHTTP)
	mux.HandleFunc("/v1/auth/whoami", handlers.Whoami)

	adminStore := admin.NewStore()

	dbPath := os.Getenv("GATEWAY_DB_PATH")
	if dbPath == "" {
		dbPath = "gateway.db"
	}
	db, err := storage.Open(dbPath)
	var runtimeSettings *storage.RuntimeSettings
	if err != nil {
		logger.Warn("storage open failed", "error", err)
	} else {
		runtimeSettings = storage.NewRuntimeSettings(db)
	}
	adminHandlers := handlers.NewAdminHandlers(adminStore, runtimeSettings)

	s := &Server{cfg: cfg, logger: logger, db: db}

	mux.HandleFunc("/api/admin/setup/status", adminHandlers.SetupStatus)
	mux.HandleFunc("/api/admin/setup", adminHandlers.Setup)
	mux.HandleFunc("/api/admin/auth/login", adminHandlers.Login)
	mux.HandleFunc("/api/admin/auth/session", adminHandlers.Session)
	mux.HandleFunc("/api/admin/auth/logout", adminHandlers.Logout)
	mux.HandleFunc("/api/admin/runtime", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			adminHandlers.GetRuntime(w, r)
		case http.MethodPut:
			adminHandlers.PutRuntime(w, r)
		case http.MethodDelete:
			adminHandlers.DeleteRuntime(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if !adminHandlers.RequireAdmin(w, r) {
				return
			}
			adminHandlers.ListUsers(w, r)
			return
		}
		if r.Method == http.MethodPost {
			if !adminHandlers.RequireAdmin(w, r) {
				return
			}
			adminHandlers.CreateUser(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// Prefer a native messages provider; otherwise bridge /v1/messages over a
	// chat-completions provider.
	hasMessagesProvider := false
	for _, p := range cfg.EffectiveProviders() {
		if p.APIType == config.ApiTypeMessages {
			hasMessagesProvider = true
			break
		}
	}
	var messagesHandler http.Handler
	if hasMessagesProvider {
		messagesHandler = handlers.NewPassthroughHandler(cfg, logger, "/messages", []config.ApiType{config.ApiTypeMessages})
	} else {
		messagesHandler = handlers.NewMessagesBridgeHandler(cfg, logger)
	}
	mux.HandleFunc("/v1/messages", messagesHandler.ServeHTTP)

	hasResponsesProvider := false
	for _, p := range cfg.EffectiveProviders() {
		if p.APIType == config.ApiTypeResponses {
			hasResponsesProvider = true
			break
		}
	}
	var responsesHandler http.Handler
	if hasResponsesProvider {
		responsesHandler = handlers.NewPassthroughHandler(cfg, logger, "/responses", []config.ApiType{config.ApiTypeResponses})
	} else {
		responsesHandler = handlers.NewResponsesBridgeHandler(cfg, logger)
	}
	mux.HandleFunc("/v1/responses", responsesHandler.ServeHTTP)

	// Middleware order (outermost -> innermost): Recover -> RequestID -> Logging -> mux.
	// Recover is outermost to catch panics from RequestID; Logging folds its own
	// recovery for the inner chain. RequestID must wrap Logging so the request
	// context carries the id. Do not reorder.
	handler := auth.Middleware(cfg)(mux)
	handler = middleware.Logging(logger)(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.Recover(logger)(handler)

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
			if s.db != nil {
				s.db.Close()
			}
			return err
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			if s.db != nil {
				s.db.Close()
			}
			return err
		}
		if s.db != nil {
			s.db.Close()
		}
		return nil
	}
}
