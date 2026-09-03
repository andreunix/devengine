// Package secrets provides domain-neutral primitives for opaque secrets,
// one-way digests, and encryption at rest of small values.
//
// It deliberately does not define OAuth, identity, MFA, or credential
// semantics. Callers remain responsible for the lifecycle and policy of the
// values they protect.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	// DefaultOpaqueSize preserves the 256-bit opaque-secret profile used by
	// consumers that do not need a different size.
	DefaultOpaqueSize = 32
	// KeySize is the required AES-256 key size.
	KeySize = 32
)

var (
	ErrInvalidSize       = errors.New("secrets: opaque size must be greater than zero")
	ErrInvalidKey        = errors.New("secrets: key must contain exactly 32 bytes")
	ErrNotConfigured     = errors.New("secrets: cipher is not configured")
	ErrInvalidCiphertext = errors.New("secrets: invalid ciphertext")
)

// Generator creates Base64URL opaque values. Its zero value uses crypto/rand.
// NewGenerator exists for deterministic tests; production callers should use
// the zero value or NewOpaque.
type Generator struct{ reader io.Reader }

// NewGenerator returns a generator backed by reader. A nil reader retains the
// production crypto/rand source.
func NewGenerator(reader io.Reader) Generator { return Generator{reader: reader} }

// NewOpaque returns size random bytes encoded with unpadded Base64URL.
func NewOpaque(size int) (string, error) { return (Generator{}).NewOpaque(size) }

// NewOpaque returns size random bytes encoded with unpadded Base64URL.
func (g Generator) NewOpaque(size int) (string, error) {
	if size <= 0 {
		return "", ErrInvalidSize
	}
	reader := g.reader
	if reader == nil {
		reader = rand.Reader
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", fmt.Errorf("secrets: generate opaque value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Codec is the minimal compatibility-oriented interface for consumers that
// use the default 256-bit opaque profile. Reader is intended for deterministic
// tests; leave it nil in production.
type Codec struct {
	Reader io.Reader
}

// NewSecret returns a DefaultOpaqueSize opaque value.
func (c Codec) NewSecret() (string, error) {
	return NewGenerator(c.Reader).NewOpaque(DefaultOpaqueSize)
}

// Hash returns the stable lowercase hexadecimal SHA-256 digest of value.
func (Codec) Hash(value string) string { return SHA256(value) }

// SHA256 returns the lowercase hexadecimal SHA-256 digest of value.
func SHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Cipher encrypts small secrets with AES-256-GCM. Ciphertexts use the stable
// nonce || sealed-value format so existing values remain decryptable.
type Cipher struct {
	aead   cipher.AEAD
	random io.Reader
}

// NewCipher creates a Cipher and copies key into the AES implementation.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: initialize AES-GCM: %w", err)
	}
	return &Cipher{aead: aead, random: rand.Reader}, nil
}

// Encrypt returns nonce || ciphertext || authentication-tag.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, ErrNotConfigured
	}
	nonce := make([]byte, c.aead.NonceSize(), c.aead.NonceSize()+len(plaintext)+c.aead.Overhead())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return nil, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt authenticates and decrypts a value produced by Encrypt.
func (c *Cipher) Decrypt(ciphertext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, ErrNotConfigured
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize+c.aead.Overhead() {
		return nil, ErrInvalidCiphertext
	}
	plaintext, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}

func (*Cipher) String() string     { return "secrets.Cipher{aes-256-gcm}" }
func (c *Cipher) GoString() string { return c.String() }
