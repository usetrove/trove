package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"net/http"
)

// credentialHeaders are the request headers that can carry an API key. They are
// listed here, in the credential package, so nothing outside it reads them.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

// Fingerprinter tells whether two requests carry the same credential without
// keeping the credential. Its digest is an HMAC-SHA256 under a key that is made
// when the process starts and exists only in its memory, so the digest means
// nothing outside this process, cannot be reversed to a key, and is not the same
// after a restart (CLAUDE.md rule 2).
//
// A caller must treat the digest as a secret too: compare it, never log it, store
// it, return it or put it in a row.
type Fingerprinter struct{ key [32]byte }

// NewFingerprinter makes a Fingerprinter with a fresh random key. If the system
// cannot supply randomness it returns an error and the caller does without.
func NewFingerprinter() (*Fingerprinter, error) {
	var f Fingerprinter
	if _, err := rand.Read(f.key[:]); err != nil {
		return nil, err
	}
	return &f, nil
}

// Of returns the digest of the credential headers of h. Requests with no
// credential all share the digest of nothing.
func (f *Fingerprinter) Of(h http.Header) [32]byte {
	m := hmac.New(sha256.New, f.key[:])
	for _, name := range credentialHeaders {
		for _, v := range h.Values(name) {
			m.Write([]byte(name))
			m.Write([]byte{0})
			m.Write([]byte(v))
			m.Write([]byte{0})
		}
	}
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out
}
