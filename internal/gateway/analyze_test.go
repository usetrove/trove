package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestAnalysisFillsMetadataColumnsWithoutPromptText(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "anthropic_body.json"))
	}))
	dup := strings.Repeat("SENTINEL_TOOL_OUTPUT line of output\n", 40)
	body := `{"model":"claude-opus-5-5","system":"SENTINEL_SYSTEM","tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[` +
		`{"role":"user","content":"SENTINEL_USER"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{"path":"/SENTINEL/PATH"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":` + quote(dup) + `}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"b","name":"Read","input":{"path":"/SENTINEL/PATH"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":` + quote(dup) + `}]}]}`
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(body))
	row := g.rec.wait(t)
	if len(row.SegmentTokenEstimate) == 0 || !strings.Contains(string(row.Findings), "duplicate_tool_result") || !strings.Contains(string(row.ShadowSavings), "duplicate_result_collapser") {
		t.Fatalf("analysis missing from the row: est=%s findings=%s shadow=%s", row.SegmentTokenEstimate, row.Findings, row.ShadowSavings)
	}
	for _, col := range [][]byte{row.SegmentTokenEstimate, row.Findings, row.ShadowSavings} {
		if strings.Contains(string(col), "SENTINEL") {
			t.Fatalf("prompt text reached a stored column: %s", col)
		}
	}
}

func TestErrorResponsesAreNotAnalyzed(t *testing.T) {
	g := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`))
	row := g.rec.wait(t)
	if row.StatusCode != 429 || len(row.SegmentTokenEstimate) != 0 {
		t.Fatalf("an error response should not be analyzed: %+v", row)
	}
}

func quote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		if r == '\n' {
			sb.WriteString(`\n`)
		} else {
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
