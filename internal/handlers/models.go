package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/devstroop/ai-gateway/internal/config"
)

type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type ModelsHandler struct {
	Cfg    config.Config
	Logger *slog.Logger
}

func NewModelsHandler(cfg config.Config, logger *slog.Logger) *ModelsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ModelsHandler{Cfg: cfg, Logger: logger}
}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}
	seen := map[string]bool{}
	ownedBy := h.Cfg.OwnedBy
	if ownedBy == "" {
		ownedBy = "ai-gateway"
	}
	// Listed IDs are the raw provider model names. Chat routing also accepts
	// configured prefixes (stripped) and ModelMap aliases, so clients may use
	// either the advertised ID or an alias.
	entries := []ModelEntry{}
	for _, p := range h.Cfg.EffectiveProviders() {
		if p.APIType != "" && p.APIType != config.ApiTypeChatCompletions {
			continue
		}
		for _, m := range p.Models {
			id := m
			if id == "*" {
				// Resolve catch-all providers to the configured default model.
				// If no concrete default is configured, the provider contributes
				// no advertised model (documented limitation).
				if h.Cfg.DefaultModel != "" && h.Cfg.DefaultModel != "*" {
					id = h.Cfg.DefaultModel
				} else {
					continue
				}
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			entries = append(entries, ModelEntry{
				ID:      id,
				Object:  "model",
				OwnedBy: ownedBy,
			})
		}
	}
	// Embedding models are advertised as bare ids owned by the gateway.
	// Providers with "*" or empty model lists contribute no discoverable
	// entries (they act as catch-alls at request time).
	for _, p := range h.Cfg.EffectiveProviders() {
		if p.APIType != config.ApiTypeEmbeddings {
			continue
		}
		if strings.TrimSpace(p.URL) == "" {
			continue
		}
		for _, m := range p.Models {
			if m == "*" {
				continue
			}
			if seen[m] {
				continue
			}
			seen[m] = true
			entries = append(entries, ModelEntry{
				ID:      m,
				Object:  "model",
				OwnedBy: ownedBy,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   entries,
	}); err != nil {
		h.Logger.Error("failed to encode models response", "error", err)
	}
}
