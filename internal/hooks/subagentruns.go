package hooks

import (
	"strings"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/tasks"
)

// EventSubagentRecord runs on Claude Code's SubagentStart and SubagentStop and records which agent type a
// subagent run was, so `trove report` can attribute the run's requests (history_requests.agent_key) to it.
// It never prints anything, so it changes nothing the agent sees.
//
// Verified 2026-11-24 on code.claude.com/docs/en/hooks.md (raw text): SubagentStart receives agent_id and
// agent_type; SubagentStop also receives agent_transcript_path and last_assistant_message, "the text content of
// the subagent's final response". On v2.1.271 or later a subagent that reports through the SubagentHandback
// tool leaves only its closing text there, so the status is then often unknown.
const EventSubagentRecord = "subagent_record"

// Spool kinds of a subagent run. They go to subagent_runs, not hook_events.
const (
	KindSubagentStart = "subagent_start"
	KindSubagentStop  = "subagent_stop"
)

// subagentRecord stores the agent type as tasks.AgentTypeLabel (a user's own agent name is never stored), the
// hashed agent id, and, at stop, the STATUS word of the reply read in memory. The reply is never stored.
func (h *handler) subagentRecord(in Input, key string) error {
	id := agentKey(in.AgentID)
	if id == "" {
		return nil
	}
	e := event{T: h.now().UnixMilli(), Key: key, Hook: tasks.AgentTypeLabel(in.AgentType), Agent: id}
	switch in.Event {
	case "SubagentStart":
		e.Kind = KindSubagentStart
	case "SubagentStop":
		e.Kind = KindSubagentStop
		e.Status = tasks.ParseStatus(in.LastAssistantMessage)
	default:
		return nil
	}
	return record(h.dir, e)
}

// agentKey hashes a subagent id the way the history reader hashes a transcript's agentId (logs.Hash
// "cc-agent"), so a run joins its requests. The hook's id may carry an "agent-" prefix (the hooks page shows
// both forms; the transcript's agentId has none, seen 2026-11-24 on Claude Code's own subagent transcripts).
func agentKey(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "agent-")
	if id == "" || len(id) > 200 {
		return ""
	}
	return logs.Hash("cc-agent", id)
}

// KindSubagentSpawn is one Agent (formerly Task) call seen by PreToolUse. It goes to subagent_spawns. It is the
// shadow of the sibling stagger (docs/sibling-stagger.md): the analysis groups same-type spawns that come close
// together and reads from the history whether the later siblings wrote or read the shared prefix. Nothing is
// held; the hook only records.
const KindSubagentSpawn = "subagent_spawn"

// recordSpawn stores the hashed call id, the agent type label and the hashed raw type. The raw type, the prompt
// and the description are never stored. A failure to record is dropped: it must never touch the call (rule 4).
func (h *handler) recordSpawn(in Input, key string) {
	id := strings.TrimSpace(in.ToolUseID)
	if id == "" || len(id) > 200 {
		return
	}
	typ := in.ToolInput.SubagentType
	_ = record(h.dir, event{T: h.now().UnixMilli(), Key: key, Kind: KindSubagentSpawn, Hook: tasks.AgentTypeLabel(typ),
		Agent: logs.Hash("cc-tool", id), TypeKey: logs.Hash("cc-agent-type", typ)})
}
