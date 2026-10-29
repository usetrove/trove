package optimize

import (
	"strings"
	"testing"
)

func bigOutput(seed string) string {
	return strings.Repeat("line of file content "+seed+"\n", 20)
}

func TestDuplicateCollapserProperties(t *testing.T) {
	same := bigOutput("a")
	applies := [][]byte{
		req{msgs: []msg{
			user(text("read it")), assistant(toolUse("t1", "read")), user(toolResult("t1", same)),
			assistant(toolUse("t2", "read")), user(toolResult("t2", same)),
		}}.body(),
		req{msgs: []msg{ // three copies, two of them collapse
			assistant(toolUse("t1", "read")), user(toolResult("t1", same)),
			assistant(toolUse("t2", "read")), user(toolResult("t2", same)),
			assistant(toolUse("t3", "read")), user(toolResult("t3", same)),
		}}.body(),
		req{msgs: []msg{ // array-form content duplicates
			user(toolResultBlocks("t1", same, "more")), user(toolResultBlocks("t2", same, "more")),
		}}.body(),
	}
	noops := [][]byte{
		req{msgs: []msg{user(toolResult("t1", same))}}.body(), // single result
		req{msgs: []msg{user(toolResult("t1", bigOutput("a"))), user(toolResult("t2", bigOutput("b")))}}.body(),
		req{msgs: []msg{user(text(same)), user(text(same))}}.body(), // not tool results
		req{msgs: nil}.body(),
	}
	checkProperties(t, DuplicateCollapser{}, Session{}, applies, noops)
}

func collapse(t *testing.T, b []byte) *Request {
	t.Helper()
	r, err := ParseRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	out, rep := DuplicateCollapser{}.Transform(t.Context(), r)
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	return out
}

func TestDuplicateCollapserOnlyFiresOnExactMatches(t *testing.T) {
	base := bigOutput("a")
	errBlock := blk{"type": "tool_result", "tool_use_id": "t2", "content": base, "is_error": true}
	imageMixed := blk{"type": "tool_result", "tool_use_id": "t2", "content": []any{text(base), blk{"type": "image", "source": "x"}}}
	tests := []struct {
		name  string
		other blk
		fires bool
	}{
		{"identical", toolResult("t2", base), true},
		{"one byte different", toolResult("t2", strings.Replace(base, "content a", "content b", 1)), false},
		{"trailing newline", toolResult("t2", base+"\n"), false},
		{"trailing space", toolResult("t2", base+" "), false},
		{"different case", toolResult("t2", strings.ToUpper(base)), false},
		{"string vs array form", toolResultBlocks("t2", base), false},
		{"is_error twin", errBlock, false},
		{"contains non-text", imageMixed, false},
		{"below minimum size", toolResult("t2", "tiny"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := req{msgs: []msg{user(toolResult("t1", base)), user(tt.other)}}.body()
			out := collapse(t, body)
			if changed := string(out.Raw) != string(body); changed != tt.fires {
				t.Errorf("fired=%v, want %v\n%s", changed, tt.fires, out.Raw)
			}
		})
	}
}

func TestDuplicateCollapserKeepsFirstAndPointsAtIt(t *testing.T) {
	same := bigOutput("a")
	body := req{msgs: []msg{
		user(toolResult("toolu_first", same)),
		user(toolResult("toolu_second", same)),
		user(toolResult("toolu_third", same)),
	}}.body()
	out := collapse(t, body)
	texts := func(i int) string {
		s, _ := out.DecodeString(out.Messages[i].Blocks[0].Texts[0])
		return s
	}
	if texts(0) != same {
		t.Error("the first occurrence must be left alone")
	}
	for _, i := range []int{1, 2} {
		note := texts(i)
		if !strings.Contains(note, "message 1") || !strings.Contains(note, "toolu_first") {
			t.Errorf("message %d note does not point at the first result: %q", i+1, note)
		}
	}
	if out.Messages[1].Blocks[0].ToolUseID != "toolu_second" {
		t.Error("the tool_use_id must be preserved so the API still pairs the result")
	}
}

func TestDuplicateCollapserKeepsCacheControl(t *testing.T) {
	same := bigOutput("a")
	b2 := toolResult("t2", same)
	b2["cache_control"] = map[string]string{"type": "ephemeral"}
	body := req{msgs: []msg{user(toolResult("t1", same)), user(b2)}}.body()
	out := collapse(t, body)
	if !strings.Contains(string(out.Raw), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("an existing cache marker was dropped: %s", out.Raw)
	}
}
