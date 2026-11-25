package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/devstroop/ai-gateway/internal/admin"
	"github.com/devstroop/ai-gateway/internal/storage"
)

type AdminHandlers struct {
	Store   *admin.Store
	Runtime *storage.RuntimeSettings
}

func NewAdminHandlers(store *admin.Store, runtime *storage.RuntimeSettings) *AdminHandlers {
	return &AdminHandlers{Store: store, Runtime: runtime}
}

func (h *AdminHandlers) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}
	if !validRequestOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Invalid request origin")
		return
	}
	if !allowRateLimit(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	sess, u, err := h.Store.AuthenticateAndCreateSession(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
		return
	}
	_ = u
	http.SetCookie(w, &http.Cookie{
		Name:     "gw_admin_session",
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"csrf_token": sess.CSRFToken,
		"username":   u.Username,
		"role":       u.Role,
	})
}

func (h *AdminHandlers) Session(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}
	c, err := r.Cookie("gw_admin_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not logged in")
		return
	}
	sess, ok := h.Store.GetSession(c.Value)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	}
	u, ok := h.Store.GetUser(sess.Username)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "User not found")
		return
	}
	if u.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "Admin role required")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"username":   sess.Username,
		"csrf_token": sess.CSRFToken,
	})
}

func (h *AdminHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}
	if !validRequestOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Invalid request origin")
		return
	}
	if c, err := r.Cookie("gw_admin_session"); err == nil {
		if sess, ok := h.Store.GetSession(c.Value); ok {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "csrf", "Invalid CSRF token")
				return
			}
		}
		h.Store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "gw_admin_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandlers) SetupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"setup_required": !h.Store.SetupComplete(),
	})
}

func (h *AdminHandlers) Setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}
	if !validRequestOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Invalid request origin")
		return
	}
	if !allowRateLimit(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	sess, err := h.Store.SetupAdminSession(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, admin.ErrSetupComplete) {
			writeError(w, http.StatusConflict, "setup_complete", "Setup already completed")
		} else {
			writeError(w, http.StatusBadRequest, "setup_failed", err.Error())
		}
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "gw_admin_session", Value: sess.ID, Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"csrf_token": sess.CSRFToken})
}

func (h *AdminHandlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}
	if !requireAdmin(h.Store, w, r) {
		return
	}
	users := h.Store.ListUsers()
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{"username": u.Username, "role": u.Role})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"users": out})
}

func (h *AdminHandlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}
	if !requireAdmin(h.Store, w, r) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	if err := h.Store.CreateUser(req.Username, req.Password, req.Role); err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func adminSessionFromRequest(store *admin.Store, r *http.Request) (admin.Session, bool) {
	c, err := r.Cookie("gw_admin_session")
	if err != nil {
		return admin.Session{}, false
	}
	return store.GetSession(c.Value)
}

func requireAdmin(store *admin.Store, w http.ResponseWriter, r *http.Request) bool {
	if !validRequestOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Invalid request origin")
		return false
	}
	sess, ok := adminSessionFromRequest(store, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Admin login required")
		return false
	}
	u, ok := store.GetUser(sess.Username)
	if !ok || u.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "Admin role required")
		return false
	}
	// CSRF check for state-changing methods
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "csrf", "Invalid CSRF token")
			return false
		}
	}
	return true
}

func (h *AdminHandlers) RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	return requireAdmin(h.Store, w, r)
}

type rateBucket struct {
	count int
	reset time.Time
}

var (
	rateMu      sync.Mutex
	rateBuckets sync.Map
)

func allowRateLimit(ip string) bool {
	now := time.Now()
	rateMu.Lock()
	defer rateMu.Unlock()
	// Evict expired buckets to bound memory.
	rateBuckets.Range(func(key, value any) bool {
		if b, ok := value.(*rateBucket); ok && now.After(b.reset) {
			rateBuckets.Delete(key)
		}
		return true
	})
	v, _ := rateBuckets.Load(ip)
	b, _ := v.(*rateBucket)
	if b == nil || now.After(b.reset) {
		b = &rateBucket{count: 0, reset: now.Add(5 * time.Minute)}
		rateBuckets.Store(ip, b)
	}
	if b.count >= 5 {
		return false
	}
	b.count++
	return true
}

func clientIP(r *http.Request) string {
	if trustProxy() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" && net.ParseIP(ip) != nil {
					return ip
				}
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" && net.ParseIP(xri) != nil {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *AdminHandlers) GetRuntime(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	if h.Runtime == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "Runtime storage not configured")
		return
	}
	dm, found, err := h.Runtime.Get("default_model")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Failed to read runtime settings")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"default_model": dm,
		"found":         found,
	})
}

func (h *AdminHandlers) PutRuntime(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	if h.Runtime == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "Runtime storage not configured")
		return
	}
	var req struct {
		DefaultModel string `json:"default_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	if req.DefaultModel == "" {
		if err := h.Runtime.Delete("default_model"); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Failed to store")
			return
		}
	} else {
		if err := h.Runtime.Set("default_model", req.DefaultModel); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Failed to store")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandlers) DeleteRuntime(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	if h.Runtime == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "Runtime storage not configured")
		return
	}
	if err := h.Runtime.Delete("default_model"); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Failed to delete runtime setting")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandlers) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(h.Store, w, r) {
			return
		}
		next(w, r)
	}
}

func trustProxy() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GATEWAY_TRUST_PROXY")))
	return v == "1" || v == "true" || v == "yes"
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !trustProxy() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func validRequestOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (curl, server-to-server) may omit Origin.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return false
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	expected := scheme + "://" + r.Host
	return strings.EqualFold(u.Scheme+"://"+u.Host, expected)
}
