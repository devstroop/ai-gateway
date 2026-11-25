package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devstroop/ai-gateway/internal/config"
)

func TestChatProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Providers = []config.ProviderConfig{{
		ID:            "default",
		Vendor:        "openai",
		APIType:       config.ApiTypeChatCompletions,
		Models:        []string{"*"},
		URL:           upstream.URL,
		Authorization: "Bearer test",
	}}

	h := NewChatHandler(cfg, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[]}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	b, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(b), "assistant") {
		t.Fatalf("body = %s", string(b))
	}
}
