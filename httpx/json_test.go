package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"ok","extra":true}`))
	rec := httptest.NewRecorder()
	var target struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(rec, req, &target, 1024); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestDecodeJSONRejectsMultipleValues(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"one"} {"name":"two"}`))
	rec := httptest.NewRecorder()
	var target struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(rec, req, &target, 1024); err == nil {
		t.Fatal("expected multiple-value error")
	}
}
