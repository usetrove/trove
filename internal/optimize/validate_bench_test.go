package optimize

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// largeRequest is a realistic long session: a system prompt, tool definitions and many tool calls with their
// outputs. It is about the size of a long Claude Code session body.
func largeRequest(targetBytes int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"claude-sonnet-5-5","max_tokens":4096,"system":"`)
	b.WriteString(strings.Repeat("You are a coding agent working in the user's repository. ", 400))
	b.WriteString(`","tools":[{"name":"Read","description":"read a file","input_schema":{"type":"object"}}],"messages":[`)
	b.WriteString(`{"role":"user","content":[{"type":"text","text":"continue the task"}]}`)
	// The newlines are JSON escapes, not raw line breaks: a raw newline inside a string is not valid JSON.
	out := strings.Repeat(`ok   pkg3  0.012s\nsrc/a.go:12: a line of tool output\n`, 40)
	for i := 0; b.Len() < targetBytes; i++ {
		fmt.Fprintf(&b, `,{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"go test ./..."}}]}`, i)
		fmt.Fprintf(&b, `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"%s"}]}`, i, out)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

var benchSizes = []int{100 << 10, 1 << 20, 8 << 20}

// BenchmarkValidateWarm is the full validation (json.Valid and the depth scan) on a body whose bytes are already
// in cache, the case of a body the engine has just read.
func BenchmarkValidateWarm(b *testing.B) {
	for _, n := range benchSizes {
		body := largeRequest(n)
		b.Run(fmt.Sprintf("%dKB", len(body)>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				if err := validateBody(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkValidateCold copies the body before each validation, so the bytes are not in cache. The copy is
// outside the timed region.
func BenchmarkValidateCold(b *testing.B) {
	for _, n := range benchSizes {
		src := largeRequest(n)
		b.Run(fmt.Sprintf("%dKB", len(src)>>10), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				body := bytes.Clone(src)
				b.StartTimer()
				if err := validateBody(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkScanWarm is the existing scan alone, for the baseline the validation is added to.
func BenchmarkScanWarm(b *testing.B) {
	for _, n := range benchSizes {
		body := largeRequest(n)
		b.Run(fmt.Sprintf("%dKB", len(body)>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				if _, err := scanRequest(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParseRequestCold is the whole path the engine takes on an input: validation plus scan, cold.
func BenchmarkParseRequestCold(b *testing.B) {
	for _, n := range benchSizes {
		src := largeRequest(n)
		b.Run(fmt.Sprintf("%dKB", len(src)>>10), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				body := bytes.Clone(src)
				b.StartTimer()
				if _, err := ParseRequest(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMaxDepthWarm is the nesting scan alone, the part of validation added on top of encoding/json.
func BenchmarkMaxDepthWarm(b *testing.B) {
	for _, n := range benchSizes {
		body := largeRequest(n)
		b.Run(fmt.Sprintf("%dKB", len(body)>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				_ = maxDepth(body)
			}
		})
	}
}
