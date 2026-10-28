package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// fakeKey is a made-up credential; it must never appear in any output.
const fakeKey = "sk-ant-api03-FAKEFAKEFAKE1234567890abcdef"

func assertNoLeak(t *testing.T, path, out string) {
	t.Helper()
	if strings.Contains(out, "FAKEFAKEFAKE") || strings.Contains(out, "1234567890abcdef") {
		t.Errorf("%s leaked the key: %q", path, out)
	}
}

func TestIsSensitiveName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Authorization", true}, {"x-api-key", true}, {"X-Api-Key", true},
		{"Proxy-Authorization", true}, {"Cookie", true}, {"Set-Cookie", true},
		{"apiKey", true}, {"api_key", true}, {"OPENAI_API_KEY", true},
		{"access_token", true}, {"client_secret", true}, {"password", true},
		{"accesskey", true},
		{"input_tokens", false}, {"max_tokens", false}, {"model", false},
		{"Content-Type", false}, {"anthropic-version", false}, {"latency_ms", false},
	}
	for _, tt := range tests {
		if got := IsSensitiveName(tt.name); got != tt.want {
			t.Errorf("IsSensitiveName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestString(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"bare key", "using " + fakeKey + " now", "using [REDACTED] now"},
		{"bearer", "Authorization: Bearer " + fakeKey, "Authorization: [REDACTED] [REDACTED]"},
		{"kv equals", "x-api-key=abc123secretvalue&model=x", "x-api-key=[REDACTED]&model=x"},
		{"json", `{"api_key":"abc123secretvalue","model":"x"}`, `{"api_key":"[REDACTED]","model":"x"}`},
		{"token counts kept", "input_tokens=1200 max_tokens: 4096", "input_tokens=1200 max_tokens: 4096"},
		{"jwt", "t=eyJhbGciOi.eyJzdWIiOiIx.sig_part", "t=[REDACTED]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := String(tt.in); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHeadersDoesNotMutateInput(t *testing.T) {
	h := http.Header{}
	h.Set("X-Api-Key", fakeKey)
	h.Set("Authorization", "Bearer "+fakeKey)
	h.Set("Content-Type", "application/json")
	h.Set("X-Note", "contains "+fakeKey)

	out := Headers(h)
	assertNoLeak(t, "Headers", fmt.Sprint(out))
	if out.Get("Content-Type") != "application/json" {
		t.Error("non-sensitive header was altered")
	}
	if h.Get("X-Api-Key") != fakeKey {
		t.Error("input header was mutated; it must stay forwardable")
	}
}

type reqInfo struct {
	Model  string
	APIKey string
	Hdr    http.Header
	Note   string
}

func TestLeakThroughEveryPath(t *testing.T) {
	info := reqInfo{Model: "m", APIKey: fakeKey, Hdr: http.Header{"X-Api-Key": {fakeKey}}, Note: "key " + fakeKey}
	hdr := http.Header{"Authorization": {"Bearer " + fakeKey}}
	cause := errors.New("upstream rejected " + fakeKey)

	t.Run("slog message", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).Info("calling with " + fakeKey)
		assertNoLeak(t, "message", buf.String())
	})
	t.Run("slog string attr", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).Info("x", "note", "k="+fakeKey, "x-api-key", fakeKey)
		assertNoLeak(t, "string attr", buf.String())
	})
	t.Run("slog error attr", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).Error("x", "err", cause)
		assertNoLeak(t, "error attr", buf.String())
	})
	t.Run("slog struct attr", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).Info("x", "req", info)
		assertNoLeak(t, "struct attr", buf.String())
	})
	t.Run("slog header attr", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).Info("x", "headers", hdr)
		assertNoLeak(t, "header attr", buf.String())
	})
	t.Run("slog group", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).Info("x", slog.Group("req", slog.String("authorization", fakeKey), slog.String("n", fakeKey)))
		assertNoLeak(t, "group", buf.String())
	})
	t.Run("slog With and WithGroup", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(&buf, true).With("api_key", fakeKey, "n", fakeKey).WithGroup("g").Info("x", "v", fakeKey)
		assertNoLeak(t, "With", buf.String())
	})
	t.Run("slog JSON handler", func(t *testing.T) {
		var buf bytes.Buffer
		l := slog.New(NewHandler(slog.NewJSONHandler(&buf, nil)))
		l.Info("x", "req", info, "err", cause)
		assertNoLeak(t, "json handler", buf.String())
	})
	t.Run("wrapped error", func(t *testing.T) {
		err := Error(fmt.Errorf("doing thing: %w", cause))
		assertNoLeak(t, "Error()", err.Error())
		assertNoLeak(t, "%v", fmt.Sprintf("%v", err))
		assertNoLeak(t, "%s", fmt.Sprintf("%s", err))
		assertNoLeak(t, "%q", fmt.Sprintf("%q", err))
		if !errors.Is(err, cause) {
			t.Error("errors.Is must still see the cause")
		}
	})
	t.Run("Errorf", func(t *testing.T) {
		assertNoLeak(t, "Errorf", Errorf("bad key %s: %w", fakeKey, cause).Error())
	})
	t.Run("nil error", func(t *testing.T) {
		if Error(nil) != nil {
			t.Error("Error(nil) must be nil")
		}
	})
	t.Run("Value", func(t *testing.T) {
		assertNoLeak(t, "Value struct", fmt.Sprint(Value("req", info)))
		assertNoLeak(t, "Value named", fmt.Sprint(Value("api_key", fakeKey)))
		assertNoLeak(t, "Value err", fmt.Sprint(Value("e", cause)))
	})
	t.Run("redacting writer", func(t *testing.T) {
		var buf bytes.Buffer
		n, err := fmt.Fprintf(Writer(&buf), "fatal: %s\n", fakeKey)
		if err != nil || n == 0 {
			t.Fatalf("write failed: %d %v", n, err)
		}
		assertNoLeak(t, "writer", buf.String())
	})
	t.Run("panic output", func(t *testing.T) {
		out := FormatPanic("boom "+fakeKey, []byte("goroutine 1\nmain.f("+fakeKey+")\n"))
		assertNoLeak(t, "panic", out)
	})
	t.Run("JSON output of a redacted header map", func(t *testing.T) {
		b, _ := json.Marshal(Headers(hdr))
		assertNoLeak(t, "json headers", string(b))
	})
}

func TestNonSecretFieldsSurvive(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, false).Info("request", "model", "claude-x", "input_tokens", 42, "latency_ms", 7)
	for _, want := range []string{"claude-x", "input_tokens=42", "latency_ms=7"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output %q lost %q", buf.String(), want)
		}
	}
}
