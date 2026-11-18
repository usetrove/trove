package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/usetrove/trove/internal/gateway/session"
)

// This file is the per-agent edge of the hooks adapter: it turns Cursor's and
// Gemini CLI's hook input into the Input the handlers read (which is Claude Code's
// and Codex's shape), and turns the handlers' reply back into what each agent
// documents. It holds no optimization logic. Anything it cannot express is
// dropped (doing less is always safe), and stateFor keeps such rewrites in shadow
// so they are never counted as done.
//
// Sources, read 2026-11-17:
//   - Cursor: cursor.com/docs/agent/hooks. Common input: conversation_id,
//     generation_id, hook_event_name, model, cursor_version, workspace_roots.
//     preToolUse/postToolUse add tool_name (Shell, Read, Write, Grep, Delete,
//     Task, MCP:<name>), tool_input, tool_use_id, cwd; postToolUse adds
//     tool_output. preToolUse may answer permission, user_message,
//     agent_message, updated_input; postToolUse may answer additional_context
//     (and updated_mcp_tool_output, for MCP tools only). The per-tool keys inside
//     tool_input are not documented; Trove reads file_path and command, which a
//     live probe has to confirm.
//   - Gemini CLI: geminicli.com/docs/hooks/reference/ and /docs/reference/tools/.
//     Input: session_id, transcript_path, cwd, hook_event_name, timestamp;
//     BeforeTool/AfterTool add tool_name, tool_input; AfterTool adds
//     tool_response {llmContent, returnDisplay, error}. BeforeTool may answer
//     decision "deny" with reason, or hookSpecificOutput.tool_input (merged into
//     the arguments); AfterTool may answer decision "deny" (reason replaces the
//     result the model sees) or hookSpecificOutput.additionalContext.

// Agent names given to `trove hook --agent`.
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
	AgentCursor = "cursor"
	AgentGemini = "gemini"
)

// cursorTools and geminiTools map an agent's tool names to the ones the handlers
// and the engine use. Names not listed pass through unchanged and match nothing.
var (
	cursorTools = map[string]string{"Shell": "Bash", "Read": "Read", "Write": "Write", "Grep": "Grep"}
	geminiTools = map[string]string{
		"run_shell_command": "Bash", "read_file": "Read", "grep_search": "Grep", "search_file_content": "Grep",
		"glob": "Glob", "replace": "Edit", "write_file": "Write",
		// MCP tools are named mcp_<server>_<tool> (geminicli.com/docs/hooks/reference/).
		"mcp_trove_trove_fetch_full": fetchTool, "mcp_trove_trove_run": runTool,
	}
	cursorEvents = map[string]string{
		"preToolUse": "PreToolUse", "postToolUse": "PostToolUse", "postToolUseFailure": "PostToolUseFailure",
		"sessionStart": "SessionStart", "sessionEnd": "SessionEnd", "preCompact": "PreCompact",
	}
	geminiEvents = map[string]string{
		"BeforeTool": "PreToolUse", "AfterTool": "PostToolUse", "SessionStart": "SessionStart",
		"SessionEnd": "SessionEnd", "PreCompress": "PreCompact",
	}
)

func mapped(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}

// parseFor reads one hook input from r in the given agent's format.
func parseFor(agent string, r io.Reader) (Input, error) {
	switch agent {
	case AgentCursor:
		return parseCursor(r)
	case AgentGemini:
		return parseGemini(r)
	}
	return ParseInput(r)
}

func parseCursor(r io.Reader) (Input, error) {
	var c struct {
		ConversationID string          `json:"conversation_id"`
		GenerationID   string          `json:"generation_id"`
		Event          string          `json:"hook_event_name"`
		Model          string          `json:"model"`
		Cwd            string          `json:"cwd"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		ToolOutput     json.RawMessage `json:"tool_output"`
		Error          string          `json:"error_message"`
		IsInterrupt    bool            `json:"is_interrupt"`
	}
	if err := json.NewDecoder(io.LimitReader(r, maxInput)).Decode(&c); err != nil {
		return Input{}, err
	}
	in := Input{SessionID: c.ConversationID, TurnID: c.GenerationID, Event: mapped(cursorEvents, c.Event),
		Cwd: c.Cwd, Model: c.Model, ToolName: mapped(cursorTools, c.ToolName), ToolResponse: c.ToolOutput,
		Error: c.Error, IsInterrupt: c.IsInterrupt}
	if len(c.ToolInput) > 0 && json.Unmarshal(c.ToolInput, &in.ToolInput) != nil {
		in.ToolInput = ToolInput{}
	}
	return in, nil
}

func parseGemini(r io.Reader) (Input, error) {
	var g struct {
		SessionID    string          `json:"session_id"`
		Event        string          `json:"hook_event_name"`
		Cwd          string          `json:"cwd"`
		Source       string          `json:"source"`
		Reason       string          `json:"reason"`
		ToolName     string          `json:"tool_name"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse *struct {
			LLMContent json.RawMessage `json:"llmContent"`
			Error      json.RawMessage `json:"error"`
		} `json:"tool_response"`
	}
	if err := json.NewDecoder(io.LimitReader(r, maxInput)).Decode(&g); err != nil {
		return Input{}, err
	}
	in := Input{SessionID: g.SessionID, Event: mapped(geminiEvents, g.Event), Cwd: g.Cwd, Source: g.Source,
		Reason: g.Reason, ToolName: mapped(geminiTools, g.ToolName)}
	if len(g.ToolInput) > 0 {
		raw := g.ToolInput
		if in.ToolName == "Read" {
			raw = geminiReadInput(raw)
		}
		if json.Unmarshal(raw, &in.ToolInput) != nil {
			in.ToolInput = ToolInput{}
		}
	}
	// The model sees llmContent. Only a plain-text result is handed on: it is the one
	// shape whose replacement can be sent back (as an AfterTool reason).
	if tr := g.ToolResponse; tr != nil && len(tr.LLMContent) > 0 && tr.LLMContent[0] == '"' && isNull(tr.Error) {
		in.ToolResponse = tr.LLMContent
	}
	return in, nil
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

// geminiReadInput turns read_file's line range (start_line, end_line; 1-based,
// inclusive) into the offset and limit the engine keys reads by, so a range read
// is never mistaken for a whole-file read.
func geminiReadInput(raw json.RawMessage) json.RawMessage {
	var a struct {
		FilePath  string   `json:"file_path"`
		StartLine *lenient `json:"start_line"`
		EndLine   *lenient `json:"end_line"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return raw
	}
	m := map[string]any{"file_path": a.FilePath}
	start := lenient(1)
	if a.StartLine != nil && *a.StartLine > 0 {
		start = *a.StartLine
		m["offset"] = start
	}
	if a.EndLine != nil && *a.EndLine >= start {
		m["limit"] = *a.EndLine - start + 1
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

// expressible reports whether a rewrite of this kind for this tool can be sent to
// the agent at all. Claude Code and Codex use the handlers' own shapes; for the
// others only the replies reply() can translate count.
func expressible(agent, tool, kind string) bool {
	switch agent {
	case AgentCursor:
		return kind == RewriteInput && tool == "Bash" // updated_input; no output replacement outside MCP
	case AgentGemini:
		if kind == RewriteInput {
			return tool == "Bash" // tool_input merge; read_file's range keys differ from offset/limit
		}
		return tool == "Bash" || tool == "Grep" || tool == "Glob" // text results, replaced through reason
	}
	return true
}

// canAdvise reports whether the agent shows the model text a pre-tool hook adds
// without blocking the call. Neither Cursor's preToolUse nor Gemini CLI's
// BeforeTool documents such a field.
func canAdvise(agent string) bool { return agent != AgentCursor && agent != AgentGemini }

// reply turns a handler's reply into the agent's own format. Claude Code, Codex
// and unknown agents get it unchanged.
func reply(agent string, out []byte) []byte {
	if len(out) == 0 || (agent != AgentCursor && agent != AgentGemini) {
		return out
	}
	var v struct {
		H struct {
			Event         string          `json:"hookEventName"`
			Decision      string          `json:"permissionDecision"`
			Reason        string          `json:"permissionDecisionReason"`
			Context       string          `json:"additionalContext"`
			UpdatedInput  json.RawMessage `json:"updatedInput"`
			UpdatedOutput json.RawMessage `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(out, &v) != nil {
		return nil
	}
	h := v.H
	var m map[string]any
	switch {
	case h.Decision == "deny":
		if agent == AgentCursor {
			m = map[string]any{"permission": "deny", "agent_message": h.Reason}
		} else {
			m = map[string]any{"decision": "deny", "reason": h.Reason}
		}
	case len(h.UpdatedInput) > 0:
		if agent == AgentCursor {
			m = map[string]any{"updated_input": h.UpdatedInput}
		} else {
			m = map[string]any{"hookSpecificOutput": map[string]any{"tool_input": h.UpdatedInput}}
		}
	case len(h.UpdatedOutput) > 0:
		var s string
		if agent != AgentGemini || json.Unmarshal(h.UpdatedOutput, &s) != nil {
			return nil
		}
		m = map[string]any{"decision": "deny", "reason": s}
	case h.Context != "" && h.Event == "PostToolUse":
		if agent == AgentCursor {
			m = map[string]any{"additional_context": h.Context}
		} else {
			m = map[string]any{"hookSpecificOutput": map[string]any{"additionalContext": h.Context}}
		}
	default:
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

// agentSessionKey is the hashed key a session's rows are stored under. Claude
// Code's matches the gateway and history parsers, so hook counts join their data.
func agentSessionKey(agent, id string) string {
	prefix := map[string]string{AgentCodex: "x-", AgentCursor: "u-", AgentGemini: "g-"}[agent]
	if prefix == "" {
		return session.ClaudeCodeKey(id)
	}
	sum := sha256.Sum256([]byte(agent + "\x00" + id))
	return prefix + hex.EncodeToString(sum[:])[:24]
}
