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

	"strconv"

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

	hasChatProvider := false
	for _, p := range h.Cfg.EffectiveProviders() {
		if p.APIType == "" || p.APIType == config.ApiTypeChatCompletions {
			hasChatProvider = true
			break
		}
	}
	if !hasChatProvider {
		writeError(w, http.StatusNotImplemented, "not_configured", "No chat provider configured")
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

	// Validate that the request body is a JSON object before routing.
	var bodyObj map[string]any
	if err := json.Unmarshal(body, &bodyObj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	if bodyObj == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	// Reject non-string model values to avoid type mismatches during rewriting.
	if v, ok := bodyObj["model"]; ok {
		if _, ok := v.(string); !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "model must be a string")
			return
		}
	}
	// Reject duplicate top-level model keys to avoid ambiguous rewrites.
	if countTopLevelKeys(body, "model") > 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "duplicate top-level model field")
		return
	}

	// Determine requested model for routing.
	var routeModel string
	if v, ok := bodyObj["model"].(string); ok {
		routeModel = v
	}
	requestedModel := routeModel
	if routeModel == "" {
		routeModel = h.Cfg.DefaultModel
	}
	if routeModel == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	// Apply at most one matching prefix (the longest) to avoid cascading
	// transformations.
	best := ""
	for _, p := range h.Cfg.ModelPrefixes {
		if p == "" {
			continue
		}
		if strings.HasPrefix(routeModel, p) && len(p) > len(best) {
			best = p
		}
	}
	if best != "" {
		routeModel = strings.TrimPrefix(routeModel, best)
		routeModel = strings.TrimPrefix(routeModel, "/")
	}
	if m, ok := h.Cfg.ModelMap[routeModel]; ok {
		routeModel = m
	}
	if strings.TrimSpace(routeModel) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model must not be empty")
		return
	}

	provider, ok := selectProvider(h.Cfg, routeModel)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_configured", "No provider configured")
		return
	}

	upstreamURL := strings.TrimRight(provider.URL, "/")
	if upstreamURL == "" {
		writeError(w, http.StatusNotImplemented, "not_configured", "Provider URL is not configured")
		return
	}

	// Rewrite only the top-level model field structurally, preserving nested
	// JSON values. Leave the body untouched when no rewrite is needed or the
	// payload is not a JSON object.
	if routeModel != requestedModel {
		if _, present := bodyObj["model"]; present {
			rewritten, ok := rewriteModelFieldJSON(body, routeModel)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_request", "Failed to rewrite request model")
				return
			}
			body = rewritten
		} else {
			rewritten, ok := injectModelField(body, routeModel)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_request", "Failed to inject request model")
				return
			}
			body = rewritten
		}
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
	if auth := provider.Authorization; auth != "" {
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

func selectProvider(cfg config.Config, model string) (config.ProviderConfig, bool) {
	providers := cfg.EffectiveProviders()
	if len(providers) == 0 {
		return config.ProviderConfig{}, false
	}
	// Prefer an exact model match over a catch-all wildcard, regardless of
	// configuration order.
	for _, p := range providers {
		if p.APIType != config.ApiTypeChatCompletions {
			continue
		}
		for _, m := range p.Models {
			if m == model {
				return p, true
			}
		}
	}
	for _, p := range providers {
		if p.APIType != config.ApiTypeChatCompletions {
			continue
		}
		for _, m := range p.Models {
			if m == "*" {
				return p, true
			}
		}
	}
	return config.ProviderConfig{}, false
}


func rewriteModelFieldJSON(body []byte, newModel string) ([]byte, bool) {
	start, end, ok := findTopLevelModelValue(body)
	if !ok || start >= len(body) || body[start] != '"' {
		return nil, false
	}
	replacement := []byte(strconv.Quote(newModel))
	out := make([]byte, 0, len(body)-(end-start)+len(replacement))
	out = append(out, body[:start]...)
	out = append(out, replacement...)
	out = append(out, body[end:]...)
	return out, true
}



func injectModelField(body []byte, newModel string) ([]byte, bool) {
	i := 0
	for i < len(body) && isJSONSpace(body[i]) {
		i++
	}
	if i >= len(body) || body[i] != '{' {
		return nil, false
	}
	depth := 0
	inStr := false
	closeIdx := -1
	for j := i; j < len(body); j++ {
		c := body[j]
		if inStr {
			if c == '\\' {
				j++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				closeIdx = j
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, false
	}
	empty := true
	for j := i + 1; j < closeIdx; j++ {
		if !isJSONSpace(body[j]) {
			empty = false
			break
		}
	}
	replacement := []byte(strconv.Quote(newModel))
	if empty {
		out := make([]byte, 0, len(body)+len(replacement)+12)
		out = append(out, body[:closeIdx]...)
		out = append(out, []byte(`"model": `)...)
		out = append(out, replacement...)
		out = append(out, body[closeIdx:]...)
		return out, true
	}
	end := closeIdx
	for end > i+1 && isJSONSpace(body[end-1]) {
		end--
	}
	out := make([]byte, 0, len(body)+len(replacement)+12)
	out = append(out, body[:end]...)
	out = append(out, []byte(`, "model": `)...)
	out = append(out, replacement...)
	out = append(out, body[closeIdx:]...)
	return out, true
}


func countTopLevelKeys(body []byte, keyName string) int {
	count := 0
	i := 0
	for i < len(body) && isJSONSpace(body[i]) {
		i++
	}
	if i >= len(body) || body[i] != '{' {
		return 0
	}
	i++
	for i < len(body) {
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i >= len(body) || body[i] == '}' {
			break
		}
		if body[i] != '"' {
			return count
		}
		keyStart := i
		i++
		for i < len(body) {
			if body[i] == '\\' {
				i++
				if i < len(body) {
					i++
				}
				continue
			}
			if body[i] == '"' {
				i++
				break
			}
			i++
		}
		if i <= keyStart+1 {
			return count
		}
		key := string(body[keyStart+1 : i-1])
		var unquoted string
		if err := json.Unmarshal(body[keyStart:i], &unquoted); err == nil {
			key = unquoted
		}
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i >= len(body) || body[i] != ':' {
			return count
		}
		i++
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		i = skipJSONValue(body, i)
		if key == keyName {
			count++
		}
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i < len(body) && body[i] == ',' {
			i++
		}
	}
	return count
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

func skipJSONValue(body []byte, i int) int {
	if i >= len(body) {
		return i
	}
	switch body[i] {
	case '"':
		i++
		for i < len(body) {
			if body[i] == '\\' {
				i++
				if i < len(body) {
					i++
				}
				continue
			}
			if body[i] == '"' {
				return i + 1
			}
			i++
		}
		return i
	case '{', '[':
		open := body[i]
		close := byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		inStr := false
		for i < len(body) {
			c := body[i]
			if inStr {
				if c == '\\' {
					i++
					if i < len(body) {
						i++
					}
					continue
				}
				if c == '"' {
					inStr = false
				}
				i++
				continue
			}
			if c == '"' {
				inStr = true
				i++
				continue
			}
			if c == open {
				depth++
			} else if c == close {
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	default:
		for i < len(body) && body[i] != ',' && body[i] != '}' && body[i] != ']' && !isJSONSpace(body[i]) {
			i++
		}
		return i
	}
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func findTopLevelModelValue(body []byte) (int, int, bool) {
	i := 0
	for i < len(body) && isJSONSpace(body[i]) {
		i++
	}
	if i >= len(body) || body[i] != '{' {
		return -1, -1, false
	}
	i++
	found := false
	var foundStart, foundEnd int
	for i < len(body) {
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i >= len(body) || body[i] == '}' {
			break
		}
		if body[i] != '"' {
			return -1, -1, false
		}
		keyStart := i
		i++
		for i < len(body) {
			if body[i] == '\\' {
				i++
				if i < len(body) {
					i++
				}
				continue
			}
			if body[i] == '"' {
				i++
				break
			}
			i++
		}
		if i <= keyStart+1 {
			return -1, -1, false
		}
		key := string(body[keyStart+1 : i-1])
		var unquoted string
		if err := json.Unmarshal(body[keyStart:i], &unquoted); err == nil {
			key = unquoted
		}
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i >= len(body) || body[i] != ':' {
			return -1, -1, false
		}
		i++
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		valueStart := i
		valueEnd := skipJSONValue(body, i)
		if key == "model" {
			found = true
			foundStart = valueStart
			foundEnd = valueEnd
		}
		i = valueEnd
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i < len(body) && body[i] == ',' {
			i++
		}
	}
	if !found {
		return -1, -1, false
	}
	return foundStart, foundEnd, true
}
