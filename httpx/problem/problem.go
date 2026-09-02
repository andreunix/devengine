// Package problem provides RFC 7807-style Problem Details for HTTP APIs.
// It intentionally does not expose internal errors or stack traces.
package problem

import (
	"encoding/json"
	"net/http"
)

// Problem represents an HTTP API error response following RFC 7807 conventions.
// Consumers are free to embed or adapt this type; the engine does not force
// domain code to depend on it.
type Problem struct {
	// Type is a URI identifying the problem type. Default: "about:blank".
	Type string `json:"type,omitempty"`
	// Title is a short, human-readable summary of the problem type.
	Title string `json:"title,omitempty"`
	// Status mirrors the HTTP status code.
	Status int `json:"status"`
	// Detail is a human-readable explanation specific to this occurrence.
	Detail string `json:"detail,omitempty"`
	// Code is an application-specific error code (e.g. "ERR_USER_NOT_FOUND").
	Code string `json:"code,omitempty"`
	// RequestID links the problem to a specific request for log correlation.
	RequestID string `json:"request_id,omitempty"`
}

// Write serialises p as a Problem Details JSON response.
// It sets Content-Type to application/problem+json.
func Write(w http.ResponseWriter, p Problem) {
	if p.Status == 0 {
		p.Status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Error writes a minimal problem response with the given status, code and detail.
// The request ID is extracted from the X-Request-ID response header if already set.
func Error(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	Write(w, Problem{
		Status:    status,
		Code:      code,
		Detail:    detail,
		RequestID: r.Header.Get("X-Request-ID"),
	})
}

// NotFound writes a 404 problem response.
func NotFound(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusNotFound, "NOT_FOUND", "The requested resource was not found.")
}

// BadRequest writes a 400 problem response with the given detail.
func BadRequest(w http.ResponseWriter, r *http.Request, detail string) {
	Error(w, r, http.StatusBadRequest, "BAD_REQUEST", detail)
}

// UnprocessableEntity writes a 422 problem response.
func UnprocessableEntity(w http.ResponseWriter, r *http.Request, detail string) {
	Error(w, r, http.StatusUnprocessableEntity, "UNPROCESSABLE_ENTITY", detail)
}

// InternalServerError writes a 500 problem response.
// The internal error is never included in the response body.
func InternalServerError(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "An unexpected error occurred.")
}

// Unauthorized writes a 401 problem response.
func Unauthorized(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required.")
}

// Forbidden writes a 403 problem response.
func Forbidden(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this action.")
}
