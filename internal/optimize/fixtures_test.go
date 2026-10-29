package optimize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// Synthetic request builders. Nothing here is recorded traffic, so there is
// nothing to redact.

type blk map[string]any

func text(s string) blk { return blk{"type": "text", "text": s} }
func toolUse(id, name string) blk {
	return blk{"type": "tool_use", "id": id, "name": name, "input": map[string]any{"cmd": "x"}}
}
func toolResult(id, content string) blk {
	return blk{"type": "tool_result", "tool_use_id": id, "content": content}
}
func toolResultBlocks(id string, texts ...string) blk {
	var cs []any
	for _, t := range texts {
		cs = append(cs, text(t))
	}
	return blk{"type": "tool_result", "tool_use_id": id, "content": cs}
}

type msg struct {
	role   string
	blocks []blk
}

func user(b ...blk) msg      { return msg{"user", b} }
func assistant(b ...blk) msg { return msg{"assistant", b} }

type req struct {
	model  string
	system any // string or []blk
	tools  []string
	msgs   []msg
	extra  string // raw JSON fields appended to the root, e.g. `"cache_control":{"type":"ephemeral"}`
}

// body renders the request as compact JSON with a stable key order.
func (r req) body() []byte {
	var sb strings.Builder
	model := r.model
	if model == "" {
		model = "claude-opus-4-8"
	}
	fmt.Fprintf(&sb, `{"model":%q,"max_tokens":1024`, model)
	if len(r.tools) > 0 {
		sb.WriteString(`,"tools":[`)
		for i, t := range r.tools {
			if i > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, `{"name":%q,"description":"tool","input_schema":{"type":"object"}}`, t)
		}
		sb.WriteByte(']')
	}
	if r.system != nil {
		b, _ := json.Marshal(r.system)
		sb.WriteString(`,"system":`)
		sb.Write(b)
	}
	sb.WriteString(`,"messages":[`)
	for i, m := range r.msgs {
		if i > 0 {
			sb.WriteByte(',')
		}
		b, _ := json.Marshal(m.blocks)
		fmt.Fprintf(&sb, `{"role":%q,"content":%s}`, m.role, b)
	}
	sb.WriteByte(']')
	if r.extra != "" {
		sb.WriteByte(',')
		sb.WriteString(r.extra)
	}
	sb.WriteByte('}')
	return []byte(sb.String())
}

// stub is a controllable optimizer for engine tests.
type stub struct {
	name      string
	applies   bool
	transform func(ctx context.Context, r *Request) (*Request, Report)
}

func (s stub) Name() string                   { return s.name }
func (s stub) Tier() Tier                     { return TierLossless }
func (s stub) Applies(*Request, Session) bool { return s.applies }
func (s stub) Transform(ctx context.Context, r *Request) (*Request, Report) {
	return s.transform(ctx, r)
}

// replaceFirst returns a transform that replaces the first occurrence of old.
func replaceFirst(old, repl string) func(context.Context, *Request) (*Request, Report) {
	return func(_ context.Context, r *Request) (*Request, Report) {
		i := strings.Index(string(r.Raw), old)
		if i < 0 {
			return r, Report{}
		}
		out, err := r.Apply([]Edit{{Span: Span{i, i + len(old)}, New: []byte(repl)}})
		if err != nil {
			return r, Report{Err: err.Error()}
		}
		return out, Report{}
	}
}

func testEngine(t *testing.T, states map[string]string, pipeline ...Optimizer) *Engine {
	t.Helper()
	cfg := config.Default()
	for k, v := range states {
		cfg.Optimizers[k] = v
	}
	return New(Options{Config: cfg, Pipeline: pipeline, Getenv: func(string) string { return "" }})
}

func input(body []byte) Input {
	return Input{Provider: "anthropic", Path: "/v1/messages", Body: body, SessionKey: "s1", Agent: "sdk"}
}
