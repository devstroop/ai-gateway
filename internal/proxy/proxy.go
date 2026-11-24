package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const maxCachedTransports = 64

type transportCacheEntry struct {
	tr   http.RoundTripper
	key  string
}

var (
	transportCacheMu sync.Mutex
	transportCache   = map[string]http.RoundTripper{}
	transportOrder    []string
)

func cacheKey(proxyURL string) string {
	h := sha256.Sum256([]byte(proxyURL))
	return hex.EncodeToString(h[:])
}

func NewTransport(proxyURL string) (http.RoundTripper, error) {
	key := cacheKey(proxyURL)
	transportCacheMu.Lock()
	if tr, ok := transportCache[key]; ok {
		transportCacheMu.Unlock()
		return tr, nil
	}
	transportCacheMu.Unlock()

	base := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 0,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		DisableCompression:    true,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url %q: %w", redactProxyURL(proxyURL), err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("unsupported proxy scheme: %s (only http/https supported)", u.Scheme)
		}
		base.Proxy = http.ProxyURL(u)
	}

	transportCacheMu.Lock()
	defer transportCacheMu.Unlock()
	if tr, ok := transportCache[key]; ok {
		// Lost race; discard ours.
		base.CloseIdleConnections()
		return tr, nil
	}
	// Evict oldest if at capacity.
	if len(transportCache) >= maxCachedTransports {
		if len(transportOrder) > 0 {
			oldKey := transportOrder[0]
			transportOrder = transportOrder[1:]
			if oldTr, ok := transportCache[oldKey]; ok {
				if t, ok := oldTr.(*http.Transport); ok {
					t.CloseIdleConnections()
				}
				delete(transportCache, oldKey)
			}
		}
	}
	transportCache[key] = base
	transportOrder = append(transportOrder, key)
	return base, nil
}

func redactProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<redacted>"
	}
	u.User = nil
	u.RawQuery = ""
	u.RawFragment = ""
	return u.String()
}

func NewClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	tr, err := NewTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	// Do not set a whole-request Client.Timeout: it would also bound streaming
	// response reads. Per-request context deadlines govern upstream calls.
	return &http.Client{
		Transport: tr,
		Timeout:   0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}
