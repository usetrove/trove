package usage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func run(t *testing.T, provider, path string, streamed bool, fixture string, chunk int, clean bool) Result {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	x := New(provider, path, streamed)
	if x == nil {
		t.Fatal("no extractor")
	}
	for len(b) > 0 { // feed in arbitrary chunk sizes: results must not depend on them
		n := min(chunk, len(b))
		x.Write(b[:n])
		b = b[n:]
	}
	return x.Finish(clean)
}

func TestExtract(t *testing.T) {
	tests := []struct {
		name, provider, path, fixture string
		streamed, clean               bool
		want                          Result
	}{
		{"anthropic stream", Anthropic, "/v1/messages", "anthropic_stream.sse", true, true,
			Result{Model: "claude-opus-5-5", Reported: true, UncachedInput: 2679, CacheRead: 1500, Output: 510, ToolCalls: map[string]int64{"web_search": 1}}},
		{"anthropic stream error", Anthropic, "/v1/messages", "anthropic_stream_error.sse", true, true,
			Result{Model: "claude-opus-5-5", Reported: true, UncachedInput: 25, Output: 1, ErrorClass: "stream_error:overloaded_error"}},
		{"anthropic stream cut", Anthropic, "/v1/messages", "anthropic_stream.sse", true, false,
			Result{Model: "claude-opus-5-5", Reported: true, UncachedInput: 2679, CacheRead: 1500, Output: 510, ToolCalls: map[string]int64{"web_search": 1}, ErrorClass: "stream_incomplete"}},
		{"anthropic body", Anthropic, "/v1/messages", "anthropic_body.json", false, true,
			Result{Model: "claude-opus-5-5", Reported: true, ServiceTier: "standard", UncachedInput: 10, CacheRead: 7, CacheWriteTotal: 5, Output: 20}},
		{"anthropic error body", Anthropic, "/v1/messages", "anthropic_error.json", false, true,
			Result{ErrorClass: "api_error:rate_limit_error"}},
		{"chat stream", OpenAI, "/v1/chat/completions", "chat_stream.sse", true, true,
			Result{Model: "gpt-x", ServiceTier: "default", Reported: true, UncachedInput: 200, CacheRead: 1000, Output: 300, Reasoning: 120}},
		{"chat body", OpenAI, "/v1/chat/completions", "chat_body.json", false, true,
			Result{Model: "gpt-x", ServiceTier: "default", Reported: true, UncachedInput: 40, CacheRead: 10, Output: 9, Reasoning: 2}},
	}
	for _, tc := range tests {
		for _, chunk := range []int{1, 7, 64, 1 << 20} {
			got := run(t, tc.provider, tc.path, tc.streamed, tc.fixture, chunk, tc.clean)
			got.Raw = nil // checked separately
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s (chunk %d):\n got %+v\nwant %+v", tc.name, chunk, got, tc.want)
			}
		}
	}
}

func TestRawIsProviderUsageAndHasNoText(t *testing.T) {
	r := run(t, Anthropic, "/v1/messages", true, "anthropic_stream.sse", 64, true)
	want := `{"cache_creation_input_tokens":0,"cache_read_input_tokens":1500,"input_tokens":2679,"output_tokens":510,"server_tool_use":{"web_search_requests":1}}`
	if string(r.Raw) != want {
		t.Errorf("raw usage\n got %s\nwant %s", r.Raw, want)
	}
}

func TestChatStreamWithoutUsageIsFlagged(t *testing.T) {
	x := New(OpenAI, "/v1/chat/completions", true)
	x.Write([]byte("data: {\"model\":\"m\",\"choices\":[{}],\"usage\":null}\n\ndata: [DONE]\n\n"))
	if r := x.Finish(true); r.Reported || r.ErrorClass != "usage_unavailable" {
		t.Errorf("got %+v", r)
	}
}

func TestMalformedEventsAreSkipped(t *testing.T) {
	x := New(Anthropic, "/v1/messages", true)
	x.Write([]byte("event: x\ndata: {not json\n\n: comment\n\ndata:\n\n"))
	x.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"model\":\"m\",\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n"))
	r := x.Finish(false)
	if r.UncachedInput != 4 || r.ErrorClass != "stream_incomplete" {
		t.Errorf("got %+v", r)
	}
}

func TestUnknownEndpointHasNoExtractor(t *testing.T) {
	if New(Anthropic, "/v1/models", false) != nil || New(OpenAI, "/v1/responses", true) != nil {
		t.Error("expected nil extractor (Responses is gated until its schema is verified)")
	}
}

func FuzzExtractors(f *testing.F) {
	for _, n := range []string{"anthropic_stream.sse", "anthropic_stream_error.sse", "chat_stream.sse", "anthropic_body.json", "chat_body.json"} {
		b, _ := os.ReadFile(filepath.Join("testdata", n))
		f.Add(b, 13)
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk int) {
		if chunk < 1 {
			chunk = 1
		}
		for _, x := range []Extractor{
			New(Anthropic, "/v1/messages", true), New(Anthropic, "/v1/messages", false),
			New(OpenAI, "/v1/chat/completions", true), New(OpenAI, "/v1/chat/completions", false),
		} {
			for b := data; len(b) > 0; {
				n := min(chunk, len(b))
				x.Write(b[:n])
				b = b[n:]
			}
			r := x.Finish(true)
			if r.UncachedInput < 0 || r.Output < 0 {
				t.Fatalf("negative counts: %+v", r)
			}
		}
	})
}
