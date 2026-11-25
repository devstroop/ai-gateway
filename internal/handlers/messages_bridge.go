package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/devstroop/ai-gateway-go/internal/config"
	"github.com/devstroop/ai-gateway-go/internal/proxy"
	"github.com/devstroop/ai-gateway-go/internal/resilience"
)

type MessagesBridgeHandler struct {
	Cfg    config.Config
	Logger *slog.Logger
}

func NewMessagesBridgeHandler(cfg config.Config, logger *slog.Logger) *MessagesBridgeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MessagesBridgeHandler{Cfg: cfg, Logger: logger}
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model         string             `json:"model"`
	Messages      []anthropicMessage `json:"messages"`
	System        json.RawMessage    `json:"system"`
	MaxTokens     int                `json:"max_tokens"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	StopSequences []string           `json:"stop_sequences"`
	Stream        bool               `json:"stream"`
}

func (h *MessagesBridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request body")
		return
	}
	if req.Stream {
		writeError(w, http.StatusNotImplemented, "streaming_not_supported", "Streaming is not supported for bridged providers")
		return
	}

	if req.Model == "" {
		req.Model = h.Cfg.DefaultModel
	}

	// Select a chat-completions provider (bridge path).
	provider, ok := selectProvider(h.Cfg, req.Model)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_configured", "No chat provider configured")
		return
	}

	// Convert Anthropic request to OpenAI chat request.
	openaiMessages := make([]map[string]any, 0, len(req.Messages)+1)
	if len(req.System) > 0 {
		sysText := extractAnthropicText(req.System)
		if sysText != "" {
			openaiMessages = append(openaiMessages, map[string]any{
				"role":    "system",
				"content": sysText,
			})
		}
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		openaiMessages = append(openaiMessages, map[string]any{
			"role":    role,
			"content": extractAnthropicText(m.Content),
		})
	}

	openaiReq := map[string]any{
		"model":    req.Model,
		"messages": openaiMessages,
	}
	if req.MaxTokens > 0 {
		openaiReq["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		openaiReq["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		openaiReq["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		openaiReq["stop"] = req.StopSequences
	}
	openaiBody, err := json.Marshal(openaiReq)
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
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(openaiBody))
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

	// Convert OpenAI response to Anthropic response.
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
	finish := "end_turn"
	if len(oa.Choices) > 0 {
		text = normalizeOpenAIContent(oa.Choices[0].Message.Content)
		switch oa.Choices[0].FinishReason {
		case "length":
			finish = "max_tokens"
		case "stop":
			finish = "end_turn"
		default:
			finish = "end_turn"
		}
	}
	anthropicResp := map[string]any{
		"id":            oa.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         req.Model,
		"content":       []map[string]any{{"type": "text", "text": text}},
		"stop_reason":   finish,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  oa.Usage.PromptTokens,
			"output_tokens": oa.Usage.CompletionTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(anthropicResp)
}

func extractAnthropicText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// array of content blocks
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if t, _ := b["type"].(string); t == "text" {
				if txt, _ := b["text"].(string); txt != "" {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
