package vectordata

import (
	"crypto/rand"
	"encoding/hex"
)

// NewUUIDKey generates a random version 4 UUID in canonical lowercase form.
// Providers use this for missing keys in auto-generated UUID collections.
// It does not allocate an integer sequence value or mutate caller records.
func NewUUIDKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	var text [32]byte
	hex.Encode(text[:], raw[:])
	return string(text[:8]) + "-" + string(text[8:12]) + "-" + string(text[12:16]) + "-" + string(text[16:20]) + "-" + string(text[20:]), nil
}
