package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFlagsRealShapesAndSkipsFakesAndMarkedLines(t *testing.T) {
	body := strings.Repeat("aB3dE5gH7j", 5) // 50 characters, no fake words
	cases := []struct {
		name, text string
		want       int
	}{
		{"a long key", "k = sk-ant-api03-" + body + "\n", 1},
		{"an AWS id", "id AKIA" + strings.Repeat("Q7", 8) + "\n", 1},
		{"a private key header", "-----BEGIN OPENSSH PRIVATE KEY-----\n", 1}, // secretscan:allow
		{"a short test key", "k = sk-test-1234\n", 0},
		{"a key that says fake", "k = sk-ant-api03-FAKE" + body + "\n", 0},
		{"a marked line", "k = sk-ant-api03-" + body + " // secretscan:allow\n", 0},
		{"plain prose", "the sk prefix is common\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(p, []byte(c.text), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := scan(p); got != c.want {
				t.Fatalf("scan = %d, want %d", got, c.want)
			}
		})
	}
}
