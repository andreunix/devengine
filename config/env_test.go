package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

func TestSecretValueIsRedactedByFormatSlogAndJSON(t *testing.T) {
	secret := SecretValue("actual-secret")
	var text, structured bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("test", "password", secret)
	slog.New(slog.NewJSONHandler(&structured, nil)).Info("test", "password", secret)
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{fmt.Sprint(secret), text.String(), structured.String(), string(encoded)} {
		if value == "actual-secret" || bytes.Contains([]byte(value), []byte("actual-secret")) {
			t.Fatalf("secret leaked: %q", value)
		}
	}
	if secret.Reveal() != "actual-secret" {
		t.Fatal("Reveal did not return secret")
	}
}

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
