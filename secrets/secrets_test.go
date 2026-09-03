package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func TestOpaqueCompatibilityAndInjectedEntropy(t *testing.T) {
	raw := make([]byte, DefaultOpaqueSize)
	for index := range raw {
		raw[index] = byte(index)
	}
	got, err := NewGenerator(bytes.NewReader(raw)).NewOpaque(DefaultOpaqueSize)
	if err != nil {
		t.Fatal(err)
	}
	if want := base64.RawURLEncoding.EncodeToString(raw); got != want {
		t.Fatalf("NewOpaque()=%q, want %q", got, want)
	}
	if len(got) != 43 || strings.Contains(got, "=") {
		t.Fatalf("opaque format changed: %q", got)
	}
	if _, err := NewOpaque(0); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("NewOpaque(0) error=%v", err)
	}
	if _, err := NewGenerator(failingReader{}).NewOpaque(32); err == nil || strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("entropy error=%v", err)
	}
}

func TestCodecUsesDefaultOpaqueProfile(t *testing.T) {
	raw := make([]byte, DefaultOpaqueSize)
	got, err := (Codec{Reader: bytes.NewReader(raw)}).NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if got != base64.RawURLEncoding.EncodeToString(raw) || (Codec{}).Hash("secret") != SHA256("secret") {
		t.Fatal("Codec changed the default opaque or digest profile")
	}
}

func TestSHA256Compatibility(t *testing.T) {
	const want = "2bb80d537b1da3e38bd30361aa855686bde0eacd7162fef6a25fe97bf527a25b"
	if got := SHA256("secret"); got != want {
		t.Fatalf("SHA256()=%q, want %q", got, want)
	}
}

func TestCipherPreservesFormatAndRejectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.random = bytes.NewReader(bytes.Repeat([]byte{3}, cipher.aead.NonceSize()))
	plaintext := []byte("small secret")
	ciphertext, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) != cipher.aead.NonceSize()+len(plaintext)+cipher.aead.Overhead() {
		t.Fatalf("ciphertext length=%d", len(ciphertext))
	}
	decrypted, err := cipher.Decrypt(ciphertext)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt()=(%q, %v)", decrypted, err)
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := cipher.Decrypt(ciphertext); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("tampered ciphertext error=%v", err)
	}
}

func TestCipherValidatesConfigurationWithoutExposingKey(t *testing.T) {
	key := bytes.Repeat([]byte("private"), 7)
	if _, err := NewCipher(key); !errors.Is(err, ErrInvalidKey) || strings.Contains(err.Error(), string(key)) {
		t.Fatalf("invalid key error=%v", err)
	}
	var cipher *Cipher
	if _, err := cipher.Encrypt([]byte("secret")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("zero cipher error=%v", err)
	}
}
