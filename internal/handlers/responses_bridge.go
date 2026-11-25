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

type ResponsesBridgeHandler struct {
	Cfg    config.Config
	Logger *slog.Logger
}

func NewResponsesBridgeHandler(cfg config.Config, logger *slog.Logger) *ResponsesBridgeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ResponsesBridgeHandler{Cfg: cfg, Logger: logger}
}

func (h *ResponsesBridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request body")
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		model = h.Cfg.DefaultModel
	}
	if stream, _ := req["stream"].(bool); stream {
		writeError(w, http.StatusNotImplemented, "streaming_not_supported", "Streaming is not supported for bridged providers")
		return
	}

	provider, ok := selectProvider(h.Cfg, model)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_configured", "No chat provider configured")
		return
	}

	// Convert Responses request to chat request.
	var systemParts []string
	messages := make([]map[string]any, 0)
	if instr, ok := req["instructions"].(string); ok && instr != "" {
		systemParts = append(systemParts, instr)
	}
	// input can be string or array
	if rawInput, present := req["input"]; present && rawInput != nil {
		switch v := rawInput.(type) {
		case string:
			if v != "" {
				messages = append(messages, map[string]any{"role": "user", "content": v})
			}
		case []any:
			for _, item := range v {
				switch iv := item.(type) {
				case string:
					messages = append(messages, map[string]any{"role": "user", "content": iv})
				case map[string]any:
					role, _ := iv["role"].(string)
					if role == "" {
						role = "user"
					}
					content := ""
					if c, ok := iv["content"].(string); ok {
						content = c
					} else if arr, ok := iv["content"].([]any); ok {
						var parts []string
						for _, ci := range arr {
							switch civ := ci.(type) {
							case string:
								if civ != "" {
									parts = append(parts, civ)
								}
							case map[string]any:
								if t, _ := civ["type"].(string); t != "text" && t != "input_text" {
									continue
								}
								if txt, _ := civ["text"].(string); txt != "" {
									parts = append(parts, txt)
								}
							}
						}
						content = strings.Join(parts, "\n")
					}
					if role == "system" || role == "developer" {
						if content != "" {
							systemParts = append(systemParts, content)
						}
					} else {
						messages = append(messages, map[string]any{"role": role, "content": content})
					}
				}
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "input must be a string or array")
			return
		}
	}
	if len(systemParts) > 0 {
		messages = append([]map[string]any{{"role": "system", "content": strings.Join(systemParts, "\n")}}, messages...)
	}

	chatReq := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if v, ok := req["max_output_tokens"]; ok {
		n, ok := v.(float64)
		if !ok || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "max_output_tokens must be a positive number")
			return
		}
		chatReq["max_tokens"] = int(n)
	}
	if v, ok := req["temperature"]; ok {
		chatReq["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		chatReq["top_p"] = v
	}

	reqBody, err := json.Marshal(chatReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build upstream request")
		return
	}

	breaker := resilience.Get(provider.ID)
	if !breaker.Allow() {
		writeError(w, http.StatusServiceUnavailable, "circuit_open", "Provider temporarily unavailable")
		return
	}

	upstreamURL := strings.TrimRight(provider.URL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build request")
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if auth := provider.Authorization; auth != "" {
		httpReq.Header.Set("Authorization", auth)
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
	defer func() { _ = resp.Body.Close() }()
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

	var oa struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &oa); err != nil {
		breaker.RecordFailure()
		writeError(w, http.StatusBadGateway, "upstream_error", "Invalid upstream response")
		return
	}
	breaker.RecordSuccess()
	text := ""
	if len(oa.Choices) > 0 {
		text = normalizeOpenAIContent(oa.Choices[0].Message.Content)
	}
	respObj := map[string]any{
		"id":         oa.ID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "completed",
		"model":      model,
		"output": []map[string]any{{
			"type":   "message",
			"id":     oa.ID,
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{
			"input_tokens":  oa.Usage.PromptTokens,
			"output_tokens": oa.Usage.CompletionTokens,
			"total_tokens":  oa.Usage.PromptTokens + oa.Usage.CompletionTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(respObj)
}
