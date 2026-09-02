package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// UUIDv7 returns an RFC 9562 UUID version 7 using Unix milliseconds and cryptographic randomness.
func UUIDv7() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	milliseconds := uint64(time.Now().UTC().UnixMilli())
	raw[0] = byte(milliseconds >> 40)
	raw[1] = byte(milliseconds >> 32)
	raw[2] = byte(milliseconds >> 24)
	raw[3] = byte(milliseconds >> 16)
	raw[4] = byte(milliseconds >> 8)
	raw[5] = byte(milliseconds)
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80

	var encoded [32]byte
	hex.Encode(encoded[:], raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), nil
}

func MustUUIDv7() string {
	value, err := UUIDv7()
	if err != nil {
		panic(err)
	}
	return value
}
