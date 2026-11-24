package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
)

func subagentEvent(event, agentID, agentType, reply string) string {
	enc := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":%q,"agent_id":%s,"agent_type":%s,"last_assistant_message":%s}`,
		sessionID, event, enc(agentID), enc(agentType), enc(reply))
}

// Start and stop are recorded per run with a hashed id that joins the history reader's agent_key, the user's own
// agent names are never stored, and the reply is reduced to its STATUS word.
func TestSubagentRunsAreRecordedWithoutContent(t *testing.T) {
	r := newRig(t, nil)
	const secret = "SECRET-REPLY-TEXT"
	for _, in := range []string{
		subagentEvent("SubagentStart", "agent-a1b2c3", tasks.TestRunnerName, ""),
		subagentEvent("SubagentStop", "a1b2c3", tasks.TestRunnerName, "STATUS: failed\n"+secret),
		subagentEvent("SubagentStart", "d4e5", "my-private-agent", ""),
		subagentEvent("SubagentStop", "d4e5", "my-private-agent", secret),
		subagentEvent("SubagentStart", "", "Explore", ""), // no id: nothing to join, nothing recorded
	} {
		if out, code := r.run(EventSubagentRecord, in); out != "" || code != 0 {
			t.Fatalf("the recorder must print nothing and exit 0, got %q %d", out, code)
		}
	}
	spool, err := os.ReadFile(filepath.Join(hooksDir(r.dir), spoolName))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{secret, "my-private-agent", "a1b2c3"} {
		if strings.Contains(string(spool), leak) {
			t.Fatalf("the spool holds %q:\n%s", leak, spool)
		}
	}
	db, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := IngestSpool(context.Background(), db, r.dir); err != nil {
		t.Fatal(err)
	}
	type run struct {
		typ, status string
		start, stop int64
	}
	got := map[string]run{}
	rows, err := db.Query(`SELECT agent_key, agent_type, status, started_at, stopped_at FROM subagent_runs WHERE session_key = ?`, r.key())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var x run
		if err := rows.Scan(&k, &x.typ, &x.status, &x.start, &x.stop); err != nil {
			t.Fatal(err)
		}
		got[k] = x
	}
	_ = rows.Close()
	if len(got) != 2 {
		t.Fatalf("want 2 runs, got %+v", got)
	}
	tr := got[logs.Hash("cc-agent", "a1b2c3")]
	if tr.typ != tasks.TestRunnerName || tr.status != tasks.StatusFailed || tr.start == 0 || tr.stop == 0 {
		t.Errorf("test runner run: %+v (the agent- prefix must not change the key)", tr)
	}
	if u := got[logs.Hash("cc-agent", "d4e5")]; u.typ != "user" || u.status != tasks.StatusUnknown {
		t.Errorf("a user's agent: %+v", u)
	}
}

// The routine agents are declined in the off arm of their trial, like the scout; in the on arm they go through.
func TestOffArmDeclinesRoutineAgents(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureRoutine}})
	offID, onID := armIDs(tasks.FeatureRoutine)
	r.startAs(offID)
	r.startAs(onID)
	for _, agent := range []string{tasks.TestRunnerName, tasks.LintFixerName} {
		out, code := r.run(EventSubagent, subagentInput(offID, "Agent", agent))
		if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) {
			t.Errorf("%s off arm: want a deny, got %q", agent, out)
		}
		if out, _ := r.run(EventSubagent, subagentInput(onID, "Agent", agent)); out != "" {
			t.Errorf("%s on arm must go through, got %q", agent, out)
		}
	}
	if out, _ := r.run(EventSubagent, subagentInput(offID, "Agent", tasks.ScoutName)); out != "" {
		t.Errorf("the scout is not in this trial and must go through, got %q", out)
	}
	r.snapshot(Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureRoutine}, RolledBack: []string{tasks.FeatureRoutine}})
	if out, _ := r.run(EventSubagent, subagentInput(offID, "Agent", tasks.TestRunnerName)); out != "" {
		t.Errorf("a rolled-back feature must go through, got %q", out)
	}
}

// In the off arm of the Explore trial, Trove's Explore runs on the main conversation's model; a user's own Explore
// is never touched.
func TestExploreOffArmRestoresTheMainModel(t *testing.T) {
	r := newRig(t, nil)
	r.snapshot(Snapshot{States: map[string]string{}, TaskTrial: []string{tasks.FeatureExplore}})
	cwd, transcript := subModelProject(t)
	offID, onID := armIDs(tasks.FeatureExplore)
	r.startAs(offID)
	r.startAs(onID)
	if out, _ := r.run(EventSubagent, agentCall(offID, cwd, transcript, tasks.ExploreAgent, "")); out != "" {
		t.Fatalf("without Trove's Explore file the call must go through, got %q", out)
	}
	c, err := tasks.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := tasks.ExploreFiles(c, tasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, filepath.FromSlash(fs[0].Path)), []byte(fs[0].Content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := r.run(EventSubagent, agentCall(offID, cwd, transcript, tasks.ExploreAgent, ""))
	if !strings.Contains(out, `"updatedInput"`) || !strings.Contains(out, `"model":"sonnet"`) {
		t.Errorf("off arm: want the main model back, got %q", out)
	}
	if out, _ := r.run(EventSubagent, agentCall(onID, cwd, transcript, tasks.ExploreAgent, "")); out != "" {
		t.Errorf("on arm must go through, got %q", out)
	}
	if out, _ := r.run(EventSubagent, agentCall(offID, cwd, transcript, tasks.ExploreAgent, `,"model":"opus"`)); out != "" {
		t.Errorf("a call that names a model must go through, got %q", out)
	}
}
