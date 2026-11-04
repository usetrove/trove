package usage

import (
	"reflect"
	"testing"
)

func TestAnthropicCacheWriteByTTL(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		total int64
		want  map[string]int64
	}{
		// Shape recorded from a real Claude Code request (2026-11-03): only the 5-minute TTL was used.
		{"5m only, from a real run",
			`{"input_tokens":2,"output_tokens":232,"cache_read_input_tokens":1639,"cache_creation_input_tokens":438,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":438,"ephemeral_1h_input_tokens":0}}`,
			438, map[string]int64{"5m": 438}},
		{"mixed TTLs (example from the prompt-caching docs)",
			`{"input_tokens":2048,"cache_read_input_tokens":1800,"cache_creation_input_tokens":248,"output_tokens":503,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":148,"ephemeral_1h_input_tokens":100}}`,
			248, map[string]int64{"5m": 148, "1h": 100}},
		{"no cache write needs no split",
			`{"input_tokens":4,"output_tokens":119,"cache_creation_input_tokens":0,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}`,
			0, nil},
		{"cache_creation object missing: stay unpriceable",
			`{"input_tokens":10,"cache_creation_input_tokens":5,"output_tokens":20}`, 5, nil},
		{"split does not add up to the total: stay unpriceable",
			`{"input_tokens":10,"cache_creation_input_tokens":500,"output_tokens":20,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":100}}`,
			500, nil},
		{"unknown TTL in the object: stay unpriceable",
			`{"input_tokens":10,"cache_creation_input_tokens":300,"output_tokens":20,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_24h_input_tokens":200}}`,
			300, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"type":"message","model":"claude-haiku-5-5","usage":` + tc.usage + `}`)
			got := anthropicBody(body)
			if got.CacheWriteTotal != tc.total {
				t.Fatalf("CacheWriteTotal = %d, want %d", got.CacheWriteTotal, tc.total)
			}
			if !reflect.DeepEqual(got.CacheWriteByTTL, tc.want) {
				t.Fatalf("CacheWriteByTTL = %v, want %v", got.CacheWriteByTTL, tc.want)
			}
		})
	}
}
