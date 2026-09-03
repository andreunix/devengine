package problem

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func FuzzWriteProblemDetails(f *testing.F) {
	f.Add(uint16(400), "BAD_REQUEST", "invalid input", "request-1")
	f.Add(uint16(500), "INTERNAL_ERROR", "", "")
	f.Fuzz(func(t *testing.T, rawStatus uint16, code, detail, requestID string) {
		status := int(rawStatus%900) + 100
		response := httptest.NewRecorder()
		want := Problem{Status: status, Code: code, Detail: detail, RequestID: requestID}
		Write(response, want)
		if got := response.Header().Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		var decoded Problem
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("invalid problem JSON: %v", err)
		}
		canonical, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var normalized Problem
		if err := json.Unmarshal(canonical, &normalized); err != nil {
			t.Fatal(err)
		}
		if decoded != normalized {
			t.Fatalf("decoded problem = %+v, want %+v", decoded, normalized)
		}
	})
}
