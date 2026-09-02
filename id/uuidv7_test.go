package id

import (
	"regexp"
	"testing"
)

func TestUUIDv7Shape(t *testing.T) {
	value, err := UUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(value) {
		t.Fatalf("unexpected UUIDv7 %q", value)
	}
}
