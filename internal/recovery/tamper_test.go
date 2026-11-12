package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture is the failing input from the stress suite's store-corrupted-on-disk case: the stored output with
// its first byte flipped. Its ID is the ID of the original text.
const tamperedFixture = "testdata/tampered/527452153c96f0ac.out"
const tamperedID = "527452153c96f0ac"

// Get must never return bytes that do not hash to the ID they were stored under.
func TestGetRefusesTamperedFixture(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tamperedFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tamperedID+fileExt), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, _, err := s.Get(tamperedID, 0, 0)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("tampered output: err = %v, text %q; want ErrNotFound and no text", err, got)
	}
	if got != "" {
		t.Fatalf("tampered output returned %d bytes alongside the error", len(got))
	}
	if _, statErr := os.Stat(filepath.Join(dir, tamperedID+fileExt)); !os.IsNotExist(statErr) {
		t.Fatal("the tampered file was not removed, so every later call would read it again")
	}
	if _, _, err := s.Get(tamperedID, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second call after removal: err = %v; want ErrNotFound", err)
	}
}

// A single byte changed in the middle of a large output must be caught: the check covers every byte, not a
// prefix or a sample.
func TestGetHashesEveryByte(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := []byte(strings.Repeat("line of tool output that is long enough to matter\n", 6000))
	id, err := s.Put(text)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+fileExt)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0x01 // one bit, halfway through
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(id, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a one-bit change in the middle was not caught: err = %v", err)
	}
}

// An untouched output is still returned exactly as stored.
func TestGetServesIntactOutput(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := "ok   pkg1  0.1s\nok   pkg2  0.2s\n"
	id, err := s.Put([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(id, 0, 0)
	if err != nil || got != text {
		t.Fatalf("intact output: got %q, %v; want the stored text", got, err)
	}
}
