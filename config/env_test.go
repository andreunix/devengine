package config

import (
	"testing"
	"time"
)

func TestEnvironmentParsers(t *testing.T) {
	t.Setenv("ENGINE_BOOL", "true")
	t.Setenv("ENGINE_INT", "42")
	t.Setenv("ENGINE_DURATION", "5s")
	if value, err := Bool("ENGINE_BOOL", false); err != nil || !value {
		t.Fatalf("bool: %v %v", value, err)
	}
	if value, err := Int("ENGINE_INT", 0); err != nil || value != 42 {
		t.Fatalf("int: %v %v", value, err)
	}
	if value, err := Duration("ENGINE_DURATION", 0); err != nil || value != 5*time.Second {
		t.Fatalf("duration: %v %v", value, err)
	}
}
