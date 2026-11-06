package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/adapter/plugin"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// Parity for the plugin adapter (CLAUDE.md "one engine, many adapters"): the same
// tool output must come out identical from the gateway, the hooks adapter and a
// harness plugin talking `trove engine serve`'s protocol, and a whole request
// sent through the plugin's `request` method must come out byte for byte as the
// gateway forwards it. The optimizers to test come from the capability matrix, so
// a new optimizer the plugin adapter reaches fails here until it has a fixture.
//
// The fixtures and the expected answers are also written to
// plugins/opencode/test/fixtures/parity.json (go test ./internal/gateway
// -run PluginParity -update-plugin-fixtures), where the plugin's own tests send
// the same inputs through the real `trove engine serve` and require the same
// outputs. That is what shows the plugin's glue adds no logic of its own.

var updatePluginFixtures = flag.Bool("update-plugin-fixtures", false, "rewrite plugins/opencode/test/fixtures/parity.json")

const fixturesPath = "../../plugins/opencode/test/fixtures/parity.json"

// parityBody is the request every request-level case sends: the fixture text as
// the result of a Bash tool call.
func parityBody(text string) []byte {
	enc, _ := json.Marshal(text)
	return []byte(`{"model":"claude-opus-5-5","system":"s","messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":` + string(enc) + `}]}]}`)
}

// gatewayForwarded returns the request body the gateway sends upstream.
func gatewayForwarded(t *testing.T, eng *optimize.Engine, hdr map[string]string, body []byte) []byte {
	t.Helper()
	up := &capture{}
	g := newRigWith(t, up.handler(t), func(o *Options) { o.Optimizer = eng })
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", hdr, body)
	g.rec.wait(t)
	got, _ := up.got()
	return got
}

func pluginServer(t *testing.T, name string) *plugin.Server {
	t.Helper()
	return plugin.New(parityEngine(t, name))
}

func pluginReply(t *testing.T, s *plugin.Server, m plugin.Message) plugin.Reply {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Handle(context.Background(), b)
	if !rep.OK {
		t.Fatalf("plugin protocol: %s", rep.Error)
	}
	return rep
}

// Optimizers that act only on the whole request have no tool-level fixture. Two of
// them run only for agents the user marked custom; "other" is the label the
// gateway gives an unknown User-Agent, and the plugin test sends the same label.
const customLabel = "other"

func requestOnlyBody(name string) []byte {
	pad := strings.Repeat("Rule: always explain the reason for a change before making it.\n", 100)
	same := strings.Repeat("line of file content\n", 20)
	enc := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	switch name {
	case "volatile_prefix_mover":
		return []byte(`{"model":"claude-opus-5-5","system":"You are a coding agent.\nToday's date: 2026-10-29\nBe brief.\n","messages":[{"role":"user","content":"hi"}]}`)
	case "cache_breakpoints":
		return []byte(`{"model":"claude-opus-5-5","system":` + enc(pad) + `,"tools":[{"name":"bash","description":"run","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
	case "duplicate_result_collapser":
		return []byte(`{"model":"claude-opus-5-5","system":"s","messages":[{"role":"user","content":"read it"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + enc(same) + `}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":` + enc(same) + `}]}]}`)
	}
	return nil
}

// parityEngineCustom is parityEngine with the "other" label marked custom.
func parityEngineCustom(t *testing.T, name string) *optimize.Engine {
	t.Helper()
	cfg := config.Default()
	cfg.Optimizers[name] = config.StateOn
	cfg.Optimize.CustomAgents = []string{customLabel}
	cfg.Optimize.BudgetMS = 1000
	var only []optimize.Optimizer
	for _, o := range optimize.Pipeline() {
		if o.Name() == name {
			only = append(only, o)
		}
	}
	return optimize.New(optimize.Options{Config: cfg, Pipeline: only, Getenv: func(string) string { return "" }})
}

type parityCase struct {
	Optimizer string `json:"optimizer"`
	Text      string `json:"text"`
	// ToolOutput is the text every adapter returns for a tool_output call.
	ToolOutput string `json:"tool_output"`
	// Request is the body sent as a request, and RequestOut what the gateway
	// forwarded (base64, as in the protocol).
	Request    []byte `json:"request"`
	RequestOut []byte `json:"request_out"`
}

func TestPluginParityWithGatewayAndHooks(t *testing.T) {
	fixtures := parityFixtures()
	var cases []parityCase
	for _, row := range optimize.Matrix() {
		if !row.Reaches(optimize.AdapterGateway) || !row.Reaches(optimize.AdapterPlugin) {
			t.Logf("skip %s: gateway=%s; plugin=%s", row.Optimizer, row.Support[optimize.AdapterGateway].Level, row.Support[optimize.AdapterPlugin].Level)
			continue
		}
		row := row
		t.Run(row.Optimizer, func(t *testing.T) {
			text, ok := fixtures[row.Optimizer]
			if !ok {
				if body := requestOnlyBody(row.Optimizer); body != nil {
					requestOnlyParity(t, row.Optimizer, body, &cases)
					return
				}
				t.Fatalf("%s runs on the gateway and plugin adapters but has no parity fixture; add one to parityFixtures or requestOnlyBody", row.Optimizer)
			}
			states := map[string]string{row.Optimizer: config.StateOn}
			sess := plugin.Session{Key: "parity", Harness: "opencode", Login: plugin.LoginAPIKey, States: states}

			// Tool level: plugin vs gateway vs hooks.
			rep := pluginReply(t, pluginServer(t, row.Optimizer), plugin.Message{Method: "tool_output", Session: sess,
				Call: &optimize.ToolCall{Name: "Bash"}, Output: &optimize.ToolOutput{Parts: []optimize.Part{{Text: text}}}})
			fromPlugin := rep.Output.Parts[0].Text
			fromGateway := viaGateway(t, row.Optimizer, text)
			if fromPlugin == text {
				t.Fatalf("the fixture did not make %s act through the plugin adapter", row.Optimizer)
			}
			if fromPlugin != fromGateway {
				t.Errorf("plugin and gateway disagree for %s:\nplugin:  %q\ngateway: %q", row.Optimizer, fromPlugin, fromGateway)
			}
			if row.Reaches(optimize.AdapterHooks) {
				if fromHooks, _ := viaHooks(t, row.Optimizer, text); fromHooks != fromPlugin {
					t.Errorf("plugin and hooks disagree for %s:\nplugin: %q\nhooks:  %q", row.Optimizer, fromPlugin, fromHooks)
				}
			}
			if len(rep.Reports) != 1 || !rep.Reports[0].Changed || rep.Reports[0].State != config.StateOn {
				t.Errorf("plugin report = %+v", rep.Reports)
			}

			// Request level: byte for byte what the gateway forwards.
			body := parityBody(text)
			want := gatewayForwarded(t, parityEngine(t, row.Optimizer), anthropicHdr, body)
			rr := pluginReply(t, pluginServer(t, row.Optimizer), plugin.Message{Method: "request", Session: sess,
				Request: &plugin.RequestIn{Provider: "anthropic", Path: "/v1/messages", Body: body}})
			if !bytes.Equal(rr.Request.Body, want) {
				t.Errorf("the plugin's request body differs from what the gateway forwards for %s:\nplugin:  %s\ngateway: %s", row.Optimizer, rr.Request.Body, want)
			}
			if !rr.Request.Changed {
				t.Errorf("the request-level call did not change anything for %s", row.Optimizer)
			}
			cases = append(cases, parityCase{Optimizer: row.Optimizer, Text: text, ToolOutput: fromPlugin, Request: body, RequestOut: rr.Request.Body})
		})
	}
	if len(cases) == 0 {
		t.Fatal("no optimizer is reached by both the gateway and plugin adapters; the parity test is vacuous")
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Optimizer < cases[j].Optimizer })
	b, err := json.MarshalIndent(map[string]any{
		"note":  "Generated by go test ./internal/gateway -run PluginParity -update-plugin-fixtures. Do not edit.",
		"cases": cases,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.FromSlash(fixturesPath)
	if *updatePluginFixtures {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s is missing; run go test ./internal/gateway -run PluginParity -update-plugin-fixtures", fixturesPath)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), b) {
		t.Errorf("%s is out of date; run go test ./internal/gateway -run PluginParity -update-plugin-fixtures and review the diff", fixturesPath)
	}
}

// requestOnlyParity checks an optimizer that acts only on the whole request: the
// plugin's request method must return, byte for byte, what the gateway forwards.
func requestOnlyParity(t *testing.T, name string, body []byte, cases *[]parityCase) {
	t.Helper()
	hdr := map[string]string{}
	for k, v := range anthropicHdr {
		hdr[k] = v
	}
	hdr["User-Agent"] = "opencode/1.19 (parity test)" // an unknown agent: label "other"
	want := gatewayForwarded(t, parityEngineCustom(t, name), hdr, body)
	sess := plugin.Session{Key: "parity", Harness: "opencode", Agent: customLabel, Login: plugin.LoginAPIKey}
	rr := pluginReply(t, plugin.New(parityEngineCustom(t, name)), plugin.Message{Method: "request", Session: sess,
		Request: &plugin.RequestIn{Provider: "anthropic", Path: "/v1/messages", Body: body}})
	if bytes.Equal(rr.Request.Body, body) {
		t.Fatalf("the fixture did not make %s act through the plugin adapter (skipped=%q)", name, rr.Request.Skipped)
	}
	if !bytes.Equal(rr.Request.Body, want) {
		t.Errorf("the plugin's request body differs from what the gateway forwards for %s:\nplugin:  %s\ngateway: %s", name, rr.Request.Body, want)
	}
	*cases = append(*cases, parityCase{Optimizer: name, Request: body, RequestOut: rr.Request.Body})
}
