package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usetrove/trove/internal/mcp/root"
	"github.com/usetrove/trove/internal/recovery"
)

// ServerName is the name the server registers under. Agents show its tools as
// mcp__trove__trove_read and so on, so it is also what adoption counting keys on.
const ServerName = "trove"

// maxFetchBytes caps one trove_fetch_full reply. Larger outputs are fetched in
// line ranges, so every byte stays reachable.
const maxFetchBytes = 256 << 10

// Options configures the server.
type Options struct {
	Root    *root.Root
	Store   *recovery.Store
	Trust   Trust // approvals for the project's commands file; nil approves nothing
	Version string
}

// FetchArgs are the trove_fetch_full inputs.
type FetchArgs struct {
	OutputID string `json:"output_id"`
	Start    int    `json:"start,omitempty"`
	End      int    `json:"end,omitempty"`
}

// Fetch implements trove_fetch_full.
func Fetch(st *recovery.Store, a FetchArgs) (string, error) {
	text, total, err := st.Get(strings.TrimSpace(a.OutputID), a.Start, a.End)
	if err != nil {
		return "", err
	}
	if len(text) <= maxFetchBytes {
		if a.Start > 0 || a.End > 0 {
			return fmt.Sprintf("(%d lines in all)\n%s", total, text), nil
		}
		return text, nil
	}
	first := a.Start
	if first < 1 {
		first = 1
	}
	body, n := joinLimited(splitLines(text), maxFetchBytes)
	last := first + n - 1
	return fmt.Sprintf("%s\n[showing lines %d-%d of %d; ask again with start=%d for the rest]\n", body, first, last, total, last+1), nil
}

// NewServer builds the MCP server with its four tools.
func NewServer(o Options) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: ServerName, Version: o.Version}, nil)
	ro := &sdk.ToolAnnotations{ReadOnlyHint: true}
	sdk.AddTool(s, &sdk.Tool{Name: "trove_read", Description: "Read a file; large files return an outline plus the symbol or lines requested.", Annotations: ro},
		func(_ context.Context, _ *sdk.CallToolRequest, a ReadArgs) (*sdk.CallToolResult, any, error) {
			return reply(Read(o.Root, a))
		})
	sdk.AddTool(s, &sdk.Tool{Name: "trove_search", Description: "Search project text; ranked and capped.", Annotations: ro},
		func(_ context.Context, _ *sdk.CallToolRequest, a SearchArgs) (*sdk.CallToolResult, any, error) {
			return reply(Search(o.Root, a))
		})
	sdk.AddTool(s, &sdk.Tool{Name: "trove_run", Description: "Run an allowlisted project command; returns a short result and output_id."},
		func(ctx context.Context, _ *sdk.CallToolRequest, a RunArgs) (*sdk.CallToolResult, any, error) {
			return reply(Run(ctx, o.Root, o.Store, o.Trust, a))
		})
	sdk.AddTool(s, &sdk.Tool{Name: "trove_fetch_full", Description: "Full text of output Trove shortened, by output_id.", Annotations: ro},
		func(_ context.Context, _ *sdk.CallToolRequest, a FetchArgs) (*sdk.CallToolResult, any, error) {
			return reply(Fetch(o.Store, a))
		})
	return s
}

// reply turns a tool's text or error into a result. A failure is reported to the
// agent as a tool error with a plain-English message, never as a crash.
func reply(text string, err error) (*sdk.CallToolResult, any, error) {
	if err != nil {
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil, nil
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil, nil
}

// Serve runs the server over stdin and stdout until the client disconnects.
// Stdout carries the protocol, so nothing else may write to it.
func Serve(ctx context.Context, o Options) error {
	return NewServer(o).Run(ctx, &sdk.StdioTransport{})
}

// SchemaSize is the size of what the agent receives in tools/list, which is
// sent with every request.
type SchemaSize struct {
	Tools           int            `json:"tools"`
	Bytes           int            `json:"bytes"`
	EstimatedTokens int            `json:"estimated_tokens"`
	Label           string         `json:"label"`
	PerTool         map[string]int `json:"estimated_tokens_per_tool"`
}

// bytesPerToken matches the ratio the rest of Trove uses before it has
// calibrated against a model. The count is an estimate; an exact one needs a
// provider's token counter, which Trove does not call.
const bytesPerToken = 3.5

// MeasureSchema lists the tools through a real in-memory MCP session and sizes
// the JSON an agent would receive.
func MeasureSchema(ctx context.Context, o Options) (SchemaSize, error) {
	ct, st := sdk.NewInMemoryTransports()
	srv := NewServer(o)
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return SchemaSize{}, err
	}
	defer func() { _ = ss.Close() }()
	cl := sdk.NewClient(&sdk.Implementation{Name: "trove-schema-size", Version: "0"}, nil)
	cs, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		return SchemaSize{}, err
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return SchemaSize{}, err
	}
	out := SchemaSize{Tools: len(res.Tools), Label: "estimated", PerTool: map[string]int{}}
	for _, t := range res.Tools {
		b, err := json.Marshal(t)
		if err != nil {
			return SchemaSize{}, err
		}
		out.Bytes += len(b)
		out.PerTool[t.Name] = estTokens(len(b))
	}
	out.EstimatedTokens = estTokens(out.Bytes)
	return out, nil
}

func estTokens(n int) int { return int(math.Ceil(float64(n) / bytesPerToken)) }
