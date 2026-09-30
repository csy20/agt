package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns prefix plus 16 random hex characters.
func New(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return prefix + hex.EncodeToString(buf[:])
}

// TraceID is 16 bytes, hex-encoded, suitable as an OTLP trace id.
func TraceID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
