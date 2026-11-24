package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"strings"
	"time"

	"github.com/devstroop/ai-gateway-go/internal/config"
	"github.com/devstroop/ai-gateway-go/internal/middleware"
)

const maxResponseBodyBytes = 100 * 1024 * 1024

var errRequestBodyTooLarge = errors.New("request body too large")

type ChatHandler struct {
	Cfg    config.Config
	Logger *slog.Logger
	Client *http.Client
}

func NewChatHandler(cfg config.Config, logger *slog.Logger) *ChatHandler {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 0, // disabled; request context governs timeouts
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:         100,
		MaxIdleConnsPerHost:  100,
		DisableCompression:   true,
	}
	return &ChatHandler{
		Cfg:    cfg,
		Logger: logger,
		Client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}

	upstreamURL := strings.TrimRight(h.Cfg.Upstream.URL, "/")
	if upstreamURL == "" {
		writeError(w, http.StatusNotImplemented, "not_configured", "Upstream is not configured")
		return
	}

	readTimeout := h.Cfg.Timeout()
	if readTimeout <= 0 {
		readTimeout = 30 * time.Second
	}
	readCtx, readCancel := context.WithTimeout(r.Context(), readTimeout)
	body, err := readBodyWithContext(readCtx, r.Body, 32*1024*1024)
	readCancel()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "request_timeout", "Request body read timed out")
			return
		}
		if errors.Is(err, errRequestBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Empty request body")
		return
	}

	target := upstreamURL + "/chat/completions"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	reqCtx := r.Context()
	var cancel context.CancelFunc
	timeout := h.Cfg.Timeout()
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	isStream := false
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err == nil {
		if v, ok := parsed["stream"]; ok {
			switch val := v.(type) {
			case bool:
				isStream = val
			case float64:
				isStream = val != 0
			case string:
				isStream = strings.EqualFold(strings.TrimSpace(val), "true")
			}
		}
	}
	reqCtx, cancel = context.WithTimeout(reqCtx, timeout)
	if cancel != nil {
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build upstream request")
		return
	}
	// Forward client headers, then apply gateway overrides.
	connVals := connectionHeaderValues(r.Header)
	for k, vals := range r.Header {
		if isHopByHopHeader(k, connVals) {
			continue
		}
		canon := canonicalHeader(k)
		// Do not forward client Host or Authorization; the upstream host comes
		// from the configured URL and credentials are gateway-controlled.
		if canon == "Host" || canon == "Authorization" || canon == "Content-Length" {
			continue
		}
		// Strip client-supplied credential headers.
		if isCredentialHeader(canon) {
			continue
		}
		// Strip client-supplied forwarding headers to prevent spoofing.
		if isForwardingHeader(canon) {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := h.Cfg.Upstream.Authorization; auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		if errors.Is(err, context.Canceled) || errors.Is(reqCtx.Err(), context.Canceled) {
			h.logger().Info("client canceled upstream request", "url", redactURL(target))
			return
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			h.logger().Warn("upstream request timed out", "error", sanitizeError(err, target), "url", redactURL(target))
			writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "Upstream request timed out")
			return
		}
		h.logger().Error("upstream request failed", "error", sanitizeError(err, target), "url", redactURL(target))
		writeError(w, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return
	}
	defer resp.Body.Close()

	// Propagate client/context cancellation to the upstream read.
	done := make(chan struct{})
	go func() {
		select {
		case <-reqCtx.Done():
			resp.Body.Close()
		case <-done:
		}
	}()
	defer close(done)

	if cl := resp.Header.Get("Content-Length"); cl != "" {
		var n int64
		if _, err := fmt.Sscanf(cl, "%d", &n); err == nil && n > maxResponseBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "response_too_large", "Upstream response too large")
			return
		}
	}

	if !isStream {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
		if err != nil {
			if errors.Is(err, context.Canceled) || reqCtx.Err() == context.Canceled {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) || reqCtx.Err() == context.DeadlineExceeded {
				writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "Upstream request timed out")
				return
			}
			writeError(w, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return
		}
		if int64(len(data)) > maxResponseBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "response_too_large", "Upstream response too large")
			return
		}
		respConnVals := connectionHeaderValues(resp.Header)
		for k, vals := range resp.Header {
			if isHopByHopHeader(k, respConnVals) {
				continue
			}
			canon := canonicalHeader(k)
			if canon == "Content-Length" {
				continue
			}
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
		return
	}

	respConnVals := connectionHeaderValues(resp.Header)
	for k, vals := range resp.Header {
		if isHopByHopHeader(k, respConnVals) {
			continue
		}
		canon := canonicalHeader(k)
		// Do not forward framing headers that can desync the response; keep
		// Location so upstream redirects remain usable.
		if canon == "Content-Length" {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	flush := func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	} else {
		rc := http.NewResponseController(w)
		flush = func() { _ = rc.Flush() }
	}

	w.WriteHeader(resp.StatusCode)
	flush()

	buf := make([]byte, 4*1024)
	var total int64
	for {
		select {
		case <-reqCtx.Done():
			h.logger().Info("request context done", "error", reqCtx.Err())
			return
		default:
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > maxResponseBodyBytes {
				h.logger().Error("upstream response exceeded maximum size",
					"request_id", middleware.RequestIDFromContext(r.Context()))
				flush()
				return
			}
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				h.logger().Error("upstream body write failed", "error", wErr)
				return
			}
			flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				if reqCtx.Err() != nil {
					h.logger().Info("upstream body read stopped", "error", reqCtx.Err())
				} else {
					h.logger().Error("upstream body read failed", "error", readErr)
				}
			}
			break
		}
	}
}

func (h *ChatHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	errType := "invalid_request_error"
	if status >= 500 {
		errType = "server_error"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
			"code":    code,
		},
	})
}

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                 {},
	"Trailers":           {},
	"Transfer-Encoding":  {},
	"Upgrade":            {},
}

func isForwardingHeader(name string) bool {
	canon := canonicalHeader(name)
	if canon == "Forwarded" || canon == "Via" {
		return true
	}
	if strings.HasPrefix(canon, "X-Forwarded-") {
		return true
	}
	switch canon {
	case "X-Real-Ip", "X-Original-Url", "X-Rewrite-Url", "Client-Ip",
		"X-Client-Ip", "True-Client-Ip", "X-Cluster-Client-Ip",
		"X-Original-Forwarded-For", "CF-Connecting-Ip", "X-ProxyUser-Ip":
		return true
	}
	return false
}

func sanitizeError(err error, target string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, target, redactURL(target))
	return msg
}

func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		u.User = nil
		u.RawQuery = ""
		u.RawFragment = ""
		u.Fragment = ""
		u.ForceQuery = false
		return u.String()
	}
	// Fallback redaction for unparseable input.
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "@"); i >= 0 {
		if scheme := strings.Index(raw, "://"); scheme >= 0 && i > scheme {
			raw = raw[:scheme+3] + raw[i+1:]
		} else if scheme < 0 {
			// schemeless: strip leading userinfo
			raw = raw[i+1:]
		}
	}
	return raw
}

func canonicalHeader(name string) string {
	return http.CanonicalHeaderKey(name)
}

func isCredentialHeader(name string) bool {
	canon := canonicalHeader(name)
	if canon == "Idempotency-Key" {
		return false
	}
	upper := strings.ToUpper(canon)
	for _, needle := range []string{"COOKIE", "TOKEN", "SECRET", "CREDENTIAL", "PASSWORD", "BEARER", "AUTH"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	if strings.Contains(upper, "KEY") && (strings.Contains(upper, "API") || strings.Contains(upper, "ACCESS")) {
		return true
	}
	return strings.HasSuffix(upper, "-KEY") || strings.HasSuffix(upper, "API-KEY")
}

func isHopByHopHeader(name string, connectionValues []string) bool {
	if _, ok := hopByHopHeaders[canonicalHeader(name)]; ok {
		return true
	}
	for _, v := range connectionValues {
		if canonicalHeader(strings.TrimSpace(v)) == canonicalHeader(name) {
			return true
		}
	}
	return false
}

func connectionHeaderValues(h http.Header) []string {
	var vals []string
	for _, c := range h.Values("Connection") {
		for _, part := range strings.Split(c, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				vals = append(vals, part)
			}
		}
	}
	return vals
}

func readBodyWithContext(ctx context.Context, r io.ReadCloser, max int64) ([]byte, error) {
	defer r.Close()
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, max+1))
		if err == nil && int64(len(data)) > max {
			err = errRequestBodyTooLarge
		}
		ch <- result{data: data, err: err}
	}()
	// Prefer an already-produced result over cancellation.
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return res.data, nil
	default:
	}
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return res.data, nil
	case <-ctx.Done():
		select {
		case res := <-ch:
			if res.err != nil {
				return nil, res.err
			}
			return res.data, nil
		default:
		}
		_ = r.Close()
		// Do not consume a post-close result; it may be partial/EOF.
		return nil, ctx.Err()
	}
}
