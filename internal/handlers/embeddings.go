package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/devstroop/ai-gateway-go/internal/config"
	"github.com/devstroop/ai-gateway-go/internal/proxy"
	"github.com/devstroop/ai-gateway-go/internal/resilience"
)

type EmbeddingsHandler struct {
	Cfg    config.Config
	Logger *slog.Logger
}

func NewEmbeddingsHandler(cfg config.Config, logger *slog.Logger) *EmbeddingsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmbeddingsHandler{Cfg: cfg, Logger: logger}
}

func (h *EmbeddingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Empty request body")
		return
	}

	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request body")
		return
	}

	providers := h.Cfg.EffectiveProviders()
	var embeddings []config.ProviderConfig
	for _, p := range providers {
		if p.APIType == config.ApiTypeEmbeddings {
			embeddings = append(embeddings, p)
		}
	}
	// usable
	var usable []config.ProviderConfig
	for _, p := range embeddings {
		if strings.TrimSpace(p.URL) != "" {
			usable = append(usable, p)
		}
	}
	if len(usable) == 0 {
		writeError(w, http.StatusNotImplemented, "embeddings_not_configured", "Embeddings endpoint is not configured")
		return
	}

	// select provider by model: exact match first, then wildcard ("*"),
	// then empty-models providers. When no model is supplied, prefer an
	// empty-models (all-models) provider, then the first usable one.
	var selected config.ProviderConfig
	selectedSet := false
	if parsed.Model != "" {
		// exact
		for _, p := range usable {
			for _, m := range p.Models {
				if m == parsed.Model {
					selected = p
					selectedSet = true
					break
				}
			}
			if selectedSet {
				break
			}
		}
		// wildcard
		if !selectedSet {
			for _, p := range usable {
				for _, m := range p.Models {
					if m == "*" {
						selected = p
						selectedSet = true
						break
					}
				}
				if selectedSet {
					break
				}
			}
		}
		// empty-models (treated as all models)
		if !selectedSet {
			for _, p := range usable {
				if len(p.Models) == 0 {
					selected = p
					selectedSet = true
					break
				}
			}
		}
		if !selectedSet {
			// No provider supports the requested model.
			writeError(w, http.StatusNotFound, "model_not_found", "The model does not exist or is not an embedding model.")
			return
		}
	} else {
		for _, p := range usable {
			if len(p.Models) == 0 {
				selected = p
				selectedSet = true
				break
			}
		}
		if !selectedSet {
			selected = usable[0]
			selectedSet = true
		}
	}

	breaker := resilience.Get(selected.ID)
	if !breaker.Allow() {
		writeError(w, http.StatusServiceUnavailable, "circuit_open", "Provider temporarily unavailable")
		return
	}

	target := strings.TrimRight(strings.TrimSpace(selected.URL), "/") + "/v1/embeddings"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := selected.Authorization; auth != "" {
		req.Header.Set("Authorization", auth)
	}

	client, err := proxy.NewClient(selected.Proxy, h.Cfg.Timeout())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build client")
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || r.Context().Err() != nil {
			return
		}
		breaker.RecordFailure()
		writeError(w, http.StatusBadGateway, "upstream_error", "Embedding sidecar unreachable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		breaker.RecordFailure()
	} else if resp.StatusCode < 400 {
		breaker.RecordSuccess()
	}

	respConnVals := connectionHeaderValues(resp.Header)
	for k, vals := range resp.Header {
		if isHopByHopHeader(k, respConnVals) {
			continue
		}
		canon := canonicalHeader(k)
		if canon == "Content-Length" || canon == "Content-Encoding" || canon == "Transfer-Encoding" {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil && r.Context().Err() == nil {
		h.Logger.Error("upstream body copy failed", "error", err)
		breaker.RecordFailure()
	}
}
