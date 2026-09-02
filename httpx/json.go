package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxBodyBytes is the default request body size limit (1 MiB).
const DefaultMaxBodyBytes int64 = 1 << 20

// DecodeJSON decodes a JSON request body into target. It enforces:
//   - A body size limit of maxBytes (DefaultMaxBodyBytes if ≤ 0).
//   - Rejection of unknown fields (returns an error for any field not in target).
//   - A single JSON value (returns an error if the body contains trailing data).
//
// Deprecated: Use DecodeStrict, which has the same semantics and a clearer name.
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	return DecodeStrict(w, r, target, maxBytes)
}

// DecodeStrict decodes a JSON request body into target with strict validation:
//   - Body capped at maxBytes (DefaultMaxBodyBytes if ≤ 0).
//   - Unknown fields rejected.
//   - Trailing data rejected.
//
// Returns a descriptive error for each failure mode so callers can map them
// to the appropriate HTTP status without exposing internals.
func DecodeStrict(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return fmt.Errorf("request body too large (limit %d bytes)", maxBytes)
		case errors.Is(err, io.EOF):
			return errors.New("request body is empty")
		default:
			return err // includes "unknown field" messages from DisallowUnknownFields
		}
	}

	// Ensure there is no trailing data after the first value.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("request body must contain a single JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Encode writes a JSON response with the given status code.
// It sets Content-Type to application/json and X-Content-Type-Options: nosniff.
func Encode(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}

// WriteJSON is an alias for Encode for backwards compatibility.
func WriteJSON(w http.ResponseWriter, status int, payload any) error {
	return Encode(w, status, payload)
}

// WriteError writes a minimal JSON error response.
//
// Deprecated: Prefer httpx/problem.Error for RFC 7807-style responses.
func WriteError(w http.ResponseWriter, status int, code string) error {
	return Encode(w, status, map[string]string{"error": code})
}
