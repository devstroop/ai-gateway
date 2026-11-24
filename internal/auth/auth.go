package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/devstroop/ai-gateway-go/internal/config"
)

type Identity struct {
	Name       string
	Role       string
	Owner      string
	Downstream string
}

type ctxKey string

const identityKey ctxKey = "identity"

func FromContext(ctx context.Context) (Identity, bool) {
	v, ok := ctx.Value(identityKey).(Identity)
	return v, ok
}

func Middleware(cfg config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.URL.Path == "/ready" {
				next.ServeHTTP(w, r)
				return
			}
			if !cfg.HasAuth() {
				next.ServeHTTP(w, r)
				return
			}
			token := extractToken(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing_api_key", "Missing API key")
				return
			}
			id, ok := validate(cfg, token)
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API key")
				return
			}
			ctx := context.WithValue(r.Context(), identityKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	if h := r.Header.Get("X-API-Key"); h != "" {
		return strings.TrimSpace(h)
	}
	return ""
}

func validate(cfg config.Config, token string) (Identity, bool) {
	for _, kc := range cfg.Keys {
		role := kc.Role
		if role == "" {
			role = "user"
		}
		name := kc.Name
		if name == "" {
			name = "key"
		}
		owner := kc.Owner
		if owner == "" {
			owner = name
		}
		if kc.Key != "" && token != "" && subtle.ConstantTimeCompare([]byte(kc.Key), []byte(token)) == 1 {
			return Identity{Name: name, Role: role, Owner: owner, Downstream: "key:" + name}, true
		}
		if kc.KeyHash != "" && token != "" {
			sum := sha256.Sum256([]byte(token))
			hexsum := "sha256:" + hex.EncodeToString(sum[:])
			if kc.KeyHash != "" && subtle.ConstantTimeCompare([]byte(kc.KeyHash), []byte(hexsum)) == 1 {
				return Identity{Name: name, Role: role, Owner: owner, Downstream: "key:" + name}, true
			}
		}
	}
	return Identity{}, false
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "authentication_error",
			"code":    code,
		},
	})
}
