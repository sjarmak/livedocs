package evergreen

import (
	"crypto/rand"
	"encoding/hex"
)

// NewDocumentID mints a fresh 128-bit document identifier as a lowercase
// hex string prefixed with "doc-". Callers should use this (or adapter-
// supplied upstream IDs) to set Document.ID before calling
// DocumentStore.Save — the Save contract forbids empty IDs.
func NewDocumentID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on linux reads from getrandom(2) and returns an
		// error only when the syscall is unavailable — treating that as a
		// program-crash condition is the only sensible response.
		panic("evergreen: crypto/rand.Read failed: " + err.Error())
	}
	return "doc-" + hex.EncodeToString(b[:])
}
