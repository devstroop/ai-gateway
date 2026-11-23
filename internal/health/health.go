package health

import (
	"encoding/json"
	"net/http"
)

type Status struct {
	Status string `json:"status"`
}

func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(Status{Status: "ok"})
}

// ReadyHandler returns a readiness handler. If check is nil, the endpoint
// always reports ready (process liveness only). Provide a check to reflect
// dependency availability.
func ReadyHandler(check func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := "ready"
		code := http.StatusOK
		if check != nil && !check() {
			status = "not ready"
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(Status{Status: status})
	}
}

// Ready is a convenience handler that always reports ready.
func Ready(w http.ResponseWriter, r *http.Request) {
	ReadyHandler(nil)(w, r)
}
