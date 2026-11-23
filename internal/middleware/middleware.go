package middleware

import (
	"bufio"
	"context"
	"io"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

var requestCounter atomic.Uint64

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID(id) {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				// Fall back to a time + counter based ID instead of a
				// constant zero value to avoid collisions.
				id = fmt.Sprintf("%x-%d", time.Now().UnixNano(), requestCounter.Add(1))
			} else {
				id = hex.EncodeToString(b)
			}
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// Recover catches panics from outer middleware layers (e.g. RequestID) that
// sit outside Logging. It is intended to be applied as the outermost
// middleware.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"panic", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", w.Header().Get("X-Request-Id"),
						"stack", string(debug.Stack()),
					)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"internal server error"}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"panic", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", RequestIDFromContext(r.Context()),
						"stack", string(debug.Stack()),
					)
					if !sw.committed {
						sw.Header().Set("Content-Type", "application/json")
						sw.WriteHeader(http.StatusInternalServerError)
						_, _ = sw.Write([]byte(`{"error":"internal server error"}`))
					}
				}
				reqID := RequestIDFromContext(r.Context())
				if reqID == "" {
					reqID = sw.Header().Get("X-Request-Id")
				}
				logger.Info("request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", sw.status,
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", reqID,
				)
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status    int
	committed bool
}

func (s *statusWriter) WriteHeader(code int) {
	if s.committed {
		return
	}
	s.committed = true
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.committed {
		s.committed = true
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !s.committed {
		s.committed = true
		s.status = http.StatusOK
	}
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (s *statusWriter) WriteString(str string) (int, error) {
	if !s.committed {
		s.committed = true
		s.status = http.StatusOK
	}
	if w, ok := s.ResponseWriter.(interface {
		WriteString(string) (int, error)
	}); ok {
		return w.WriteString(str)
	}
	return s.ResponseWriter.Write([]byte(str))
}

func (s *statusWriter) Flush() {
	if !s.committed {
		s.committed = true
		s.status = http.StatusOK
	}
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if !s.committed {
		s.committed = true
		s.status = http.StatusOK
	}
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(s.ResponseWriter, r)
}

// Unwrap allows http.ResponseController to reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}
