package httpx

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

func FuzzDecodeJSON(f *testing.F) {
	f.Add([]byte(`{"name":"alice"}`), uint16(1024))
	f.Add([]byte(`{"name":"alice"}{"name":"bob"}`), uint16(64))
	f.Add([]byte(`{"unknown":true}`), uint16(64))
	f.Add([]byte{}, uint16(1))

	type requestBody struct {
		Name string `json:"name"`
	}
	f.Fuzz(func(t *testing.T, body []byte, rawLimit uint16) {
		limit := int64(rawLimit%4096) + 1
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		response := httptest.NewRecorder()
		var target requestBody
		_ = DecodeJSON(response, req, &target, limit)
	})
}
