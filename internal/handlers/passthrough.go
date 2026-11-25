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
	"time"

	"github.com/devstroop/ai-gateway/internal/config"
	"github.com/devstroop/ai-gateway/internal/proxy"
	"github.com/devstroop/ai-gateway/internal/resilience"
)

type PassthroughHandler struct {
	Cfg     config.Config
	Logger  *slog.Logger
	Client  *http.Client
	Path    string
	Allowed []config.ApiType
}

func NewPassthroughHandler(cfg config.Config, logger *slog.Logger, path string, allowed []config.ApiType) *PassthroughHandler {
	if logger == nil {
		logger = slog.Default()
	}
	// Disable the client-level total timeout; per-request context governs
	// deadlines so long-running streams are not aborted mid-flight.
	return &PassthroughHandler{
		Cfg:     cfg,
		Logger:  logger,
		Client:  &http.Client{Timeout: 0},
		Path:    path,
		Allowed: allowed,
	}
}

func (h *PassthroughHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
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

	var routeModel string
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be a JSON object")
		return
	}
	if v, ok := parsed["model"]; ok {
		var m string
		if err := json.Unmarshal(v, &m); err == nil {
			routeModel = m
		}
	}
	if routeModel == "" {
		routeModel = h.Cfg.DefaultModel
	}
	for _, p := range h.Cfg.ModelPrefixes {
		routeModel = strings.TrimPrefix(routeModel, p)
		routeModel = strings.TrimPrefix(routeModel, "/")
	}
	if routeModel == "" {
		routeModel = h.Cfg.DefaultModel
	}
	if m, ok := h.Cfg.ModelMap[routeModel]; ok {
		routeModel = m
	}

	provider, ok := selectProviderForAPI(h.Cfg, routeModel, h.Allowed)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_configured", "No provider configured")
		return
	}

	// Apply routing normalization to the forwarded body so the upstream
	// receives the model name that was actually selected.
	modelRaw, err := json.Marshal(routeModel)
	if err != nil {
		h.Logger.Error("failed to normalize request body", "error", err)
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to normalize request body")
		return
	}
	parsed["model"] = json.RawMessage(modelRaw)
	newBody, err := json.Marshal(parsed)
	if err != nil {
		h.Logger.Error("failed to normalize request body", "error", err)
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to normalize request body")
		return
	}
	body = newBody

	upstreamURL := strings.TrimRight(provider.URL, "/")
	if upstreamURL == "" {
		writeError(w, http.StatusNotImplemented, "not_configured", "Provider URL is not configured")
		return
	}

	target := upstreamURL + h.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	isStream := false
	if v, ok := parsed["stream"]; ok {
		var anyVal any
		if err := json.Unmarshal(v, &anyVal); err == nil {
			switch val := anyVal.(type) {
			case bool:
				isStream = val
			case float64:
				isStream = val != 0
			case string:
				v := strings.ToLower(strings.TrimSpace(val))
				isStream = v == "true" || v == "1" || v == "yes" || v == "on"
			}
		}
	}
	effectiveCancel := cancel
	if !isStream {
		timeout := h.Cfg.Timeout()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		timeoutCtx, timeoutCancel := context.WithTimeout(reqCtx, timeout)
		reqCtx = timeoutCtx
		effectiveCancel = timeoutCancel
		defer timeoutCancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build upstream request")
		return
	}
	for k, vals := range r.Header {
		if isHopByHopHeader(k, connectionHeaderValues(r.Header)) {
			continue
		}
		canon := canonicalHeader(k)
		if canon == "Host" || canon == "Authorization" || canon == "Content-Length" || canon == "Accept-Encoding" {
			continue
		}
		if isCredentialHeader(canon) || isForwardingHeader(canon) {
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

	breaker := resilience.Get(provider.ID)
	if !breaker.Allow() {
		writeError(w, http.StatusServiceUnavailable, "circuit_open", "Provider temporarily unavailable")
		return
	}
	probeResolved := false
	defer func() {
		if !probeResolved {
			breaker.ReleaseProbe()
		}
	}()

	client, perr := proxy.NewClient(provider.Proxy, h.Cfg.Timeout())
	if perr != nil {
		breaker.RecordFailure()
		probeResolved = true
		h.Logger.Error("invalid provider proxy", "error", perr)
		writeError(w, http.StatusBadGateway, "invalid_proxy", "Provider proxy is misconfigured")
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		if errors.Is(err, context.Canceled) || errors.Is(reqCtx.Err(), context.Canceled) {
			breaker.ReleaseProbe()
			probeResolved = true
			h.Logger.Info("upstream request canceled", "error", err, "url", target)
			return
		}
		breaker.RecordFailure()
		probeResolved = true
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			h.Logger.Warn("upstream request timed out", "error", err, "url", target)
			writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "Upstream request timed out")
			return
		}
		h.Logger.Error("upstream request failed", "error", err, "url", target)
		writeError(w, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		breaker.RecordFailure()
	} else {
		breaker.RecordSuccess()
	}
	probeResolved = true

	respConnVals := connectionHeaderValues(resp.Header)
	for k, vals := range resp.Header {
		if isHopByHopHeader(k, respConnVals) {
			continue
		}
		canon := canonicalHeader(k)
		if canon == "Content-Length" || canon == "Transfer-Encoding" || canon == "Trailer" {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flush := func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	}
	buf := make([]byte, 4*1024)
	var total int64
	var activity chan struct{}
	if isStream {
		idleDuration := h.Cfg.Timeout()
		if idleDuration <= 0 {
			idleDuration = 60 * time.Second
		}
		activity = make(chan struct{}, 1)
		done := make(chan struct{})
		go func() {
			timer := time.NewTimer(idleDuration)
			defer timer.Stop()
			for {
				select {
				case <-timer.C:
					effectiveCancel()
					return
				case <-activity:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(idleDuration)
				case <-done:
					return
				case <-reqCtx.Done():
					return
				}
			}
		}()
		defer close(done)
	}
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			if !isStream && total > maxResponseBodyBytes {
				effectiveCancel()
				h.Logger.Error("upstream response exceeded maximum size")
				return
			}
			if activity != nil {
				select {
				case activity <- struct{}{}:
				default:
				}
			}
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				effectiveCancel()
				h.Logger.Error("upstream body write failed", "error", wErr)
				return
			}
			flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				h.Logger.Error("upstream body copy failed", "error", readErr)
			}
			break
		}
	}
}

func selectProviderForAPI(cfg config.Config, model string, allowed []config.ApiType) (config.ProviderConfig, bool) {
	providers := cfg.EffectiveProviders()
	if len(providers) == 0 {
		return config.ProviderConfig{}, false
	}
	// filter allowed
	var candidates []config.ProviderConfig
	for _, p := range providers {
		if len(allowed) == 0 {
			candidates = append(candidates, p)
			continue
		}
		for _, a := range allowed {
			if p.APIType == a {
				candidates = append(candidates, p)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return config.ProviderConfig{}, false
	}
	// Prefer exact model ownership over wildcard/catch-all providers.
	for _, p := range candidates {
		for _, m := range p.Models {
			if m == model {
				return p, true
			}
		}
	}
	for _, p := range candidates {
		for _, m := range p.Models {
			if m == "*" {
				return p, true
			}
		}
	}
	for _, p := range candidates {
		if len(p.Models) == 0 {
			return p, true
		}
	}
	return config.ProviderConfig{}, false
}
