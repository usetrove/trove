package auth

import (
	"bytes"
	"net/http"
	"testing"
)

func TestFingerprintTellsCredentialsApartWithoutKeepingThem(t *testing.T) {
	f, err := NewFingerprinter()
	if err != nil {
		t.Fatal(err)
	}
	key := "sk-ant-api03-DISTINCTIVEKEYMATERIAL0123456789"
	a := f.Of(http.Header{"X-Api-Key": {key}})
	if a != f.Of(http.Header{"X-Api-Key": {key}}) {
		t.Error("the same credential must give the same digest")
	}
	if a == f.Of(http.Header{"X-Api-Key": {key + "x"}}) {
		t.Error("a different credential must give a different digest")
	}
	// The header it came in is part of the identity.
	if a == f.Of(http.Header{"Authorization": {key}}) {
		t.Error("the same text in another header is not the same credential")
	}
	// The digest does not contain the key.
	if bytes.Contains(a[:], []byte("DISTINCTIVE")) {
		t.Error("the digest holds key material")
	}
	// Headers that are not credentials do not matter.
	if a != f.Of(http.Header{"X-Api-Key": {key}, "Anthropic-Version": {"2023-06-01"}, "User-Agent": {"x"}}) {
		t.Error("a header that is not a credential changed the digest")
	}
}

func TestFingerprintMeansNothingOutsideItsProcess(t *testing.T) {
	f1, _ := NewFingerprinter()
	f2, _ := NewFingerprinter()
	h := http.Header{"X-Api-Key": {"sk-ant-api03-SAMEKEY"}}
	if f1.Of(h) == f2.Of(h) {
		t.Error("two processes produced the same digest for one key, so the digest is comparable across runs")
	}
}

func TestFingerprintOfNoCredentialIsStable(t *testing.T) {
	f, _ := NewFingerprinter()
	if f.Of(http.Header{}) != f.Of(http.Header{"Content-Type": {"application/json"}}) {
		t.Error("requests with no credential should share one digest")
	}
}
