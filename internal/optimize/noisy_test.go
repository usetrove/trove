package optimize

import (
	"strings"
	"testing"
)

func TestIsNoisy(t *testing.T) {
	for cmd, want := range map[string]bool{
		"go test ./...":              true,
		"go test -v ./...":           true,
		"cd /repo && go test ./...":  true,
		"npm test":                   true,
		"pnpm run test":              true,
		"pytest":                     true,
		"python -m pytest -v":        true,
		"cargo build --verbose":      true,
		"mvn clean install":          true,
		"make":                       true,
		"go test ./internal/hooks":   false,
		"go test ./... | tail -20":   false,
		"go test ./... > out.txt":    false,
		"pytest tests/test_one.py":   false,
		"npm test && rm -rf build":   false,
		"cd /repo; go test ./...":    false,
		"echo $(go test ./...)":      false,
		"":                           false,
		strings.Repeat("make ", 100): false,
	} {
		if got := IsNoisy(cmd); got != want {
			t.Errorf("IsNoisy(%q) = %v, want %v", cmd, got, want)
		}
	}
}
