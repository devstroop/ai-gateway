package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/devstroop/ai-gateway/internal/auth"
)

func Whoami(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		id = auth.Identity{Name: "anonymous", Role: "user", Owner: "anonymous", Downstream: "anonymous"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"key": map[string]any{
			"name":       id.Name,
			"role":       id.Role,
			"owner":      id.Owner,
			"downstream": id.Downstream,
		},
	})
}
