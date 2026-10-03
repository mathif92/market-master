package httpx

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; nothing better to do.
		return
	}
}

func Err(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func Internal(w http.ResponseWriter, err error) {
	Err(w, http.StatusInternalServerError, "internal_error", err.Error())
}

// Decode reads a JSON body (max 1MiB) into v.
func Decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("empty body")
	}
	return json.Unmarshal(body, v)
}

// NotFound is the catch-all 404 handler.
func NotFound(w http.ResponseWriter, r *http.Request) {
	Err(w, http.StatusNotFound, "not_found", fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
}
