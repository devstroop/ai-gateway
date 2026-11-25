package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/devstroop/ai-gateway/internal/config"
	"github.com/devstroop/ai-gateway/internal/proxy"
	"github.com/devstroop/ai-gateway/internal/resilience"
)

type ChatBridgeHandler struct {
	Cfg    config.Config
	Logger *slog.Logger
}

func NewChatBridgeHandler(cfg config.Config, logger *slog.Logger) *ChatBridgeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ChatBridgeHandler{Cfg: cfg, Logger: logger}
}

func (h *ChatBridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	var oaiReq map[string]any
	if err := json.Unmarshal(body, &oaiReq); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request body")
		return
	}
	model, _ := oaiReq["model"].(string)
	if model == "" {
		model = h.Cfg.DefaultModel
	}
	if stream, _ := oaiReq["stream"].(bool); stream {
		writeError(w, http.StatusNotImplemented, "streaming_not_supported", "Streaming is not supported for bridged providers")
		return
	}

	provider, ok := selectProviderForMessages(h.Cfg, model)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_configured", "No messages provider configured")
		return
	}

	// Convert OpenAI chat request to Anthropic messages request.
	var systemParts []string
	messages := make([]map[string]any, 0)
	if raw, present := oaiReq["messages"]; present {
		rawMsgs, ok := raw.([]any)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "messages must be an array")
			return
		}
		for _, rm := range rawMsgs {
			m, ok := rm.(map[string]any)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_request", "each message must be an object")
				return
			}
			if r, present := m["role"]; present && r != nil {
				if _, ok := r.(string); !ok {
					writeError(w, http.StatusBadRequest, "invalid_request", "message role must be a string")
					return
				}
			}
			if c, present := m["content"]; present && c != nil {
				switch c.(type) {
				case string, []any:
				default:
					writeError(w, http.StatusBadRequest, "invalid_request", "message content must be a string or array")
					return
				}
			}
			role, _ := m["role"].(string)
			content := stringifyContent(m["content"])
			if role == "system" || role == "developer" {
				if content != "" {
					systemParts = append(systemParts, content)
				}
				continue
			}
			if role == "" {
				role = "user"
			}
			messages = append(messages, map[string]any{
				"role":    role,
				"content": content,
			})
		}
	}
	anthropicReq := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if len(systemParts) > 0 {
		anthropicReq["system"] = strings.Join(systemParts, "\n")
	}
	if v, ok := oaiReq["max_tokens"]; ok {
		n, ok := v.(float64)
		if !ok || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "max_tokens must be a positive number")
			return
		}
		anthropicReq["max_tokens"] = int(n)
	} else {
		anthropicReq["max_tokens"] = 1024
	}
	if v, ok := oaiReq["temperature"]; ok {
		anthropicReq["temperature"] = v
	}
	if v, ok := oaiReq["top_p"]; ok {
		anthropicReq["top_p"] = v
	}
	if v, ok := oaiReq["stop"]; ok {
		switch sv := v.(type) {
		case string:
			if sv != "" {
				anthropicReq["stop_sequences"] = []string{sv}
			}
		case []any:
			seqs := make([]string, 0, len(sv))
			for _, item := range sv {
				if s, ok := item.(string); ok {
					seqs = append(seqs, s)
				}
			}
			if len(seqs) > 0 {
				anthropicReq["stop_sequences"] = seqs
			}
		default:
			// ignore non-string/non-array stop values
		}
	}

	reqBody, err := json.Marshal(anthropicReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build upstream request")
		return
	}

	breaker := resilience.Get(provider.ID)
	if !breaker.Allow() {
		writeError(w, http.StatusServiceUnavailable, "circuit_open", "Provider temporarily unavailable")
		return
	}

	upstreamURL := strings.TrimRight(provider.URL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build request")
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if auth := provider.Authorization; auth != "" {
		httpReq.Header.Set("Authorization", auth)
		if strings.HasPrefix(auth, "Bearer ") {
			httpReq.Header.Set("x-api-key", strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
		} else {
			httpReq.Header.Set("x-api-key", auth)
		}
	}
	client, err := proxy.NewClient(provider.Proxy, h.Cfg.Timeout())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build client")
		return
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		breaker.RecordFailure()
		writeError(w, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err == nil && len(respBody) > maxResponseBodyBytes {
		breaker.RecordFailure()
		writeError(w, http.StatusBadGateway, "upstream_error", "Upstream response too large")
		return
	}
	if err != nil {
		breaker.RecordFailure()
		writeError(w, http.StatusBadGateway, "upstream_error", "Failed to read upstream response")
		return
	}
	if resp.StatusCode >= 500 {
		breaker.RecordFailure()
	}
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return
	}

	// Convert Anthropic response to OpenAI chat response.
	var ar struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &ar); err != nil {
		breaker.RecordFailure()
		writeError(w, http.StatusBadGateway, "upstream_error", "Invalid upstream response")
		return
	}
	breaker.RecordSuccess()
	var parts []string
	for _, c := range ar.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	text := strings.Join(parts, "")
	finish := "stop"
	if ar.StopReason == "max_tokens" {
		finish = "length"
	}
	oaiResp := map[string]any{
		"id":      ar.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     ar.Usage.InputTokens,
			"completion_tokens": ar.Usage.OutputTokens,
			"total_tokens":      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(oaiResp)
}

func selectProviderForMessages(cfg config.Config, model string) (config.ProviderConfig, bool) {
	providers := cfg.EffectiveProviders()
	// Prefer an explicit model match.
	for _, p := range providers {
		if p.APIType != config.ApiTypeMessages {
			continue
		}
		for _, m := range p.Models {
			if m == model {
				return p, true
			}
		}
	}
	// Then a wildcard provider.
	for _, p := range providers {
		if p.APIType != config.ApiTypeMessages {
			continue
		}
		for _, m := range p.Models {
			if m == "*" {
				return p, true
			}
		}
	}
	// Finally, a provider with no declared models as a catch-all.
	for _, p := range providers {
		if p.APIType != config.ApiTypeMessages {
			continue
		}
		if len(p.Models) == 0 {
			return p, true
		}
	}
	return config.ProviderConfig{}, false
}

func stringifyContent(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		var parts []string
		for _, item := range val {
			switch iv := item.(type) {
			case string:
				if iv != "" {
					parts = append(parts, iv)
				}
			case map[string]any:
				if t, _ := iv["type"].(string); t == "text" {
					if txt, _ := iv["text"].(string); txt != "" {
						parts = append(parts, txt)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
