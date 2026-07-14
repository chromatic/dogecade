package web

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// writeJSON writes v as a JSON response body, used by the /buy/status
// polling fallback endpoint.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeSSEEvent writes v as a single Server-Sent Events "data:" message.
func writeSSEEvent(w http.ResponseWriter, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE payload: %w", err)
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}
