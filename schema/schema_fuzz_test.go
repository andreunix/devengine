package schema

import (
	"bytes"
	"testing"
)

func FuzzSchemaSnapshot(f *testing.F) {
	f.Add([]byte(`{"snapshot_version":2,"captured_at":"2026-01-01T00:00:00Z","tables":{}}`))
	f.Add([]byte(`{"tables":{},"sequences":["legacy_ids"]}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{`))

	f.Fuzz(func(t *testing.T, input []byte) {
		snapshot, err := UnmarshalSnapshot(input)
		if err != nil {
			return
		}
		first, err := snapshot.MarshalToJSON()
		if err != nil {
			t.Fatalf("marshal accepted snapshot: %v", err)
		}
		roundTrip, err := UnmarshalSnapshot(first)
		if err != nil {
			t.Fatalf("unmarshal canonical snapshot: %v", err)
		}
		second, err := roundTrip.MarshalToJSON()
		if err != nil {
			t.Fatalf("remarshal canonical snapshot: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("snapshot encoding is not deterministic\nfirst: %s\nsecond: %s", first, second)
		}
	})
}
