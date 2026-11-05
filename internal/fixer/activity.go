package fixer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/claudecode"
	"github.com/usetrove/trove/internal/logs/codex"
)

// Activity says whether an agent session looks active right now.
type Activity struct {
	Active bool      `json:"active"`
	Last   time.Time `json:"last,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// CheckActivity looks for recent agent activity in two places that never hold
// prompt text: the newest request the gateway recorded, and the modification
// time of the agents' own session history files. Activity within the window
// (fixer.activity_minutes) means a session may be open, and Claude Code and Codex
// reload some config while running, so fixes wait.
func CheckActivity(ctx context.Context, e *Env) Activity {
	window := time.Duration(e.Cfg.ActivityMinutes) * time.Minute
	if window <= 0 {
		window = 30 * time.Minute
	}
	var last time.Time
	note := func(t time.Time) {
		if t.After(last) {
			last = t
		}
	}
	if e.DB != nil {
		for _, q := range []string{`SELECT MAX(ts) FROM requests`, `SELECT MAX(ts) FROM history_requests`} {
			var ms sql.NullInt64
			if err := e.DB.QueryRowContext(ctx, q).Scan(&ms); err == nil && ms.Valid {
				note(time.UnixMilli(ms.Int64))
			}
		}
	}
	home := func() (string, error) { return e.Home, nil }
	srcs := []logs.Source{
		&claudecode.Source{Getenv: e.Getenv, Home: home},
		&codex.Source{Getenv: e.Getenv, Home: home},
	}
	for _, s := range srcs {
		files, _ := s.Discover(ctx, nil)
		for _, f := range files {
			note(time.UnixMilli(f.MTime))
		}
	}
	a := Activity{Last: last}
	if !last.IsZero() && e.now().Sub(last) < window {
		a.Active = true
		a.Reason = fmt.Sprintf("An agent session was active %d minute(s) ago, and Trove does not change config files while one may be open.", int(e.now().Sub(last).Minutes()))
	}
	return a
}

// ---- queue ----------------------------------------------------------------

const queueFile = "fixqueue.json"

// Queued is a fix waiting for the agent to be idle. It holds ids and a hash of
// the file at the time, never file content.
type Queued struct {
	ID       string    `json:"id"`
	FileSHA  string    `json:"file_sha256"`
	QueuedAt time.Time `json:"queued_at"`
	// Settings is the request a settings_profile fix needs to be planned again
	// when it is applied later. A task name and a switch; no file content.
	Settings *SettingsRequest `json:"settings,omitempty"`
	// Tasks is the same for scout and cascade fixes: feature name and options.
	Tasks *TasksRequest `json:"tasks,omitempty"`
}

// LoadQueue reads the queue (empty if none).
func LoadQueue(dataDir string) ([]Queued, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, queueFile)) //nolint:gosec // inside the data dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", queueFile, err)
	}
	var q []Queued
	if err := json.Unmarshal(b, &q); err != nil {
		return nil, fmt.Errorf("%s in your Trove data directory is damaged (%v). Delete it and queue the fixes again", queueFile, err)
	}
	return q, nil
}

func saveQueue(dataDir string, q []Queued) error {
	p := filepath.Join(dataDir, queueFile)
	if len(q) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".trove-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func fileSHA(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // an allowed agent config file
	if err != nil {
		return ""
	}
	return sha(b)
}

func enqueue(e *Env, f *Fix) error {
	q, err := LoadQueue(e.DataDir)
	if err != nil {
		return err
	}
	for i := range q {
		if q[i].ID == f.ID {
			q[i].FileSHA, q[i].QueuedAt = fileSHA(f.Path), e.now().UTC()
			return saveQueue(e.DataDir, q)
		}
	}
	it := Queued{ID: f.ID, FileSHA: fileSHA(f.Path), QueuedAt: e.now().UTC()}
	if f.Kind == SettingsProfile && e.Settings != nil {
		req := *e.Settings
		it.Settings = &req
	}
	if (f.Kind == TaskScout || f.Kind == TaskCascade) && e.Tasks != nil {
		req := *e.Tasks
		it.Tasks = &req
	}
	q = append(q, it)
	return saveQueue(e.DataDir, q)
}

// ApplyQueued applies queued fixes if the agent is idle. A queued fix is dropped
// (and says why) if the evidence is gone or its file changed since it was queued.
// It returns nil, nil results when nothing is queued, and a "queued" result per
// fix when the agent is still active.
func ApplyQueued(ctx context.Context, e *Env) ([]Result, error) {
	q, err := LoadQueue(e.DataDir)
	if err != nil || len(q) == 0 {
		return nil, err
	}
	if act := CheckActivity(ctx, e); act.Active {
		var out []Result
		for _, it := range q {
			out = append(out, Result{ID: it.ID, Status: "queued", Detail: act.Reason + " It stays queued."})
		}
		return out, nil
	}
	var out []Result
	for len(q) > 0 {
		it := q[0]
		ev := e
		if it.Settings != nil || it.Tasks != nil {
			c := *e
			c.Settings, c.Tasks = it.Settings, it.Tasks
			ev = &c
		}
		fixes, _ := Detect(ctx, ev)
		f := Find(fixes, it.ID)
		var r Result
		switch {
		case f == nil:
			r = Result{ID: it.ID, Status: "skipped", Detail: "dropped from the queue: the evidence for this fix is gone"}
		case fileSHA(f.Path) != it.FileSHA:
			r = Result{ID: it.ID, Status: "skipped", Detail: "dropped from the queue: the file changed after you approved the fix. Run `trove fix " + it.ID + "` to review it again"}
		default:
			r, err = applyOne(ctx, ev, it.ID)
			if err != nil {
				_ = saveQueue(e.DataDir, q) // keep what is left
				return append(out, r), err
			}
		}
		out = append(out, r)
		q = q[1:]
		if err := saveQueue(e.DataDir, q); err != nil {
			return out, err
		}
	}
	return out, nil
}
