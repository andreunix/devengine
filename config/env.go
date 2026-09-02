package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvSource defines how environment variables are looked up.
type EnvSource interface {
	Lookup(key string) (string, bool)
}

type osEnv struct{}

func (osEnv) Lookup(key string) (string, bool) {
	return os.LookupEnv(key)
}

// Env is the active environment source. It defaults to reading from the OS,
// but can be overridden in tests.
var Env EnvSource = osEnv{}

// String returns the value of key, trimmed of whitespace.
// If the variable is unset or empty, fallback is returned.
func String(key, fallback string) string {
	value, ok := Env.Lookup(key)
	if !ok {
		return fallback
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

// SecretValue represents a sensitive string. It implements fmt.Stringer
// to redact its value when printed or logged.
type SecretValue string

func (s SecretValue) String() string {
	return "***REDACTED***"
}

// LogValue prevents structured log handlers from serializing the secret.
func (s SecretValue) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON prevents accidental serialization of the underlying value.
func (s SecretValue) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// MarshalText protects text-based serializers.
func (s SecretValue) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Reveal returns the actual sensitive string.
func (s SecretValue) Reveal() string {
	return string(s)
}

// Secret returns the value of key like String, but its value is never
// included in error messages or log attributes to prevent accidental leakage.
func Secret(key, fallback string) SecretValue {
	return SecretValue(String(key, fallback))
}

// RequiredString returns the trimmed value of key, or an error if it is
// unset or empty.
func RequiredString(key string) (string, error) {
	value, ok := Env.Lookup(key)
	trimmed := strings.TrimSpace(value)
	if !ok || trimmed == "" {
		return "", fmt.Errorf("configuration: %s is required", key)
	}
	return trimmed, nil
}

// RequiredSecret is like RequiredString but the variable is treated as a
// secret — its name (not value) appears in the error message.
func RequiredSecret(key string) (SecretValue, error) {
	value, ok := Env.Lookup(key)
	trimmed := strings.TrimSpace(value)
	if !ok || trimmed == "" {
		return "", fmt.Errorf("configuration: required secret %s is not set", key)
	}
	return SecretValue(trimmed), nil
}

// Bool parses a boolean environment variable. Accepts the values understood
// by strconv.ParseBool (1, t, T, TRUE, true, 0, f, F, FALSE, false).
func Bool(key string, fallback bool) (bool, error) {
	value, ok := Env.Lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("configuration: %s: %w", key, err)
	}
	return parsed, nil
}

// Int parses an integer environment variable.
func Int(key string, fallback int) (int, error) {
	value, ok := Env.Lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("configuration: %s: %w", key, err)
	}
	return parsed, nil
}

// Duration parses a time.Duration environment variable (e.g. "30s", "5m").
func Duration(key string, fallback time.Duration) (time.Duration, error) {
	value, ok := Env.Lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("configuration: %s: %w", key, err)
	}
	return parsed, nil
}

// CSV returns a slice of trimmed non-empty values from a comma-separated
// environment variable.
func CSV(key string) []string {
	raw, ok := Env.Lookup(key)
	if !ok {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// MustLoad collects multiple validation errors into a single error. It is
// designed to be used at application startup so all missing variables are
// reported at once rather than one at a time.
//
// Usage:
//
//	errs := config.MustLoad(
//	    func() error { dbURL, err = config.RequiredSecret("DATABASE_URL"); return err },
//	    func() error { port, err  = config.Int("PORT", 8080); return err },
//	)
//	if errs != nil { log.Fatal(errs) }
func MustLoad(loaders ...func() error) error {
	var errs []error
	for _, fn := range loaders {
		if err := fn(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Environ is a named set of key-value pairs. Use it in tests to avoid
// mutating the real process environment.
type Environ map[string]string

// Lookup implements a subset of os.LookupEnv backed by the map.
func (e Environ) Lookup(key string) (string, bool) {
	v, ok := e[key]
	return v, ok
}
