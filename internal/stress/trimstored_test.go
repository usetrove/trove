package stress

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCheckTrimStored(t *testing.T) {
	full := []byte("the full tool output\n")
	h := sha256.Sum256(full)
	id := hex.EncodeToString(h[:8])
	body := []byte(`{"messages":[{"role":"user","content":"head\n[Trove: 40 lines trimmed; full output sha256:` + id + `]\ntail"}]}`)

	if vs := CheckTrimStored(body, [][]byte{full}); len(vs) != 0 {
		t.Errorf("marker backed by its stored output was flagged: %v", vs)
	}
	if n := len(CheckTrimStored(body, nil)); n != 1 {
		t.Errorf("marker with nothing stored: want 1 violation, got %d", n)
	}
	if n := len(CheckTrimStored(body, [][]byte{[]byte("the full tool output")})); n != 1 {
		t.Errorf("marker whose stored copy differs by one byte: want 1 violation, got %d", n)
	}
	if vs := CheckTrimStored([]byte(`{"messages":[]}`), nil); len(vs) != 0 {
		t.Errorf("no markers, no violations: %v", vs)
	}
}
