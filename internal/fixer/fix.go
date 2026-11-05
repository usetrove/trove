// Package fixer proposes evidence-based edits to agent configuration files and
// applies them only with approval.
//
// Every fix is planned first (nothing is written), shown as a diff with an
// estimated saving, and applied through internal/setup so the original is backed
// up under <dataDir>/backups, recorded in changes.json and reversible byte for
// byte with `trove undo`. Fixes are never applied while an agent session is
// active (they are queued), and managed, policy and credential files are never
// touched. Every number a fix carries is labeled "estimated".
//
// Where the locations and formats come from, and what is not verified, is
// written in docs/fixer.md.
package fixer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/setup"
)

// Fix kinds.
const (
	MCPPrune     = "mcp_prune"
	ContextSlim  = "context_slim"
	CacheBreaker = "cache_breaker_fix"
	ModelDefault = "model_default"
)

// Fix is one proposed change. Nothing has been written when it is returned.
type Fix struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Agent    string   `json:"agent"` // claude_code or codex
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Why      string   `json:"why"`
	Evidence []string `json:"evidence"`
	Diff     string   `json:"diff,omitempty"`
	// TokensPerSession is the estimated prompt tokens saved in each session.
	TokensPerSession int64 `json:"est_tokens_per_session"`
	// USDPerSession is nil when it cannot be priced honestly.
	USDPerSession *float64 `json:"est_usd_per_session"`
	Label         string   `json:"label"` // always "estimated"
	// Suggestion fixes (a cheaper default model) change behavior, not just size.
	// They are never part of --all.
	Suggestion bool `json:"suggestion,omitempty"`
	// Blocked says why the fix cannot be applied (for example a managed file).
	Blocked string `json:"blocked,omitempty"`
	// Advisory is a manual step when Trove cannot make the change itself.
	Advisory string   `json:"advisory,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	// Task and Keys are set for settings_profile fixes: the task type and the
	// setting names it changes. Names only; never values or file contents.
	Task string   `json:"task,omitempty"`
	Keys []string `json:"keys,omitempty"`

	old, updated []byte
}

// Applicable reports whether the fix can be applied by Trove.
func (f *Fix) Applicable() bool { return f.Blocked == "" && f.Advisory == "" && f.updated != nil }

// Env is everything the fixes read. Nothing in it is stored.
type Env struct {
	Home       string
	ProjectDir string // the project whose files are checked
	ClaudeDir  string // ~/.claude or CLAUDE_CONFIG_DIR
	CodexDir   string // ~/.codex or CODEX_HOME
	DataDir    string
	Getenv     func(string) string
	Now        time.Time
	DB         *sql.DB // optional; fixes that need gateway or history data skip themselves without it
	Pricing    *pricing.Table
	Cfg        config.Fixer

	// Settings, when set, asks Detect to plan a settings profile for a task
	// (see settings.go). Nil leaves `trove fix` exactly as it was.
	Settings *SettingsRequest
	// SettingsVersion returns the installed harness version text ("" if unknown).
	SettingsVersion func(harness string) string
	// ReadFile overrides os.ReadFile for settings planning (tests).
	ReadFile func(string) ([]byte, error)

	usage *usageScan
}

func (e *Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

func (e *Env) now() time.Time {
	if e.Now.IsZero() {
		return time.Now()
	}
	return e.Now
}

// fixID is stable for the same kind and target, so a queued or listed id keeps
// meaning the same thing between runs.
func fixID(kind string, parts ...string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + strings.Join(parts, "\x00")))
	short := map[string]string{MCPPrune: "mcp", ContextSlim: "ctx", CacheBreaker: "cache", ModelDefault: "model", SettingsProfile: "settings"}[kind]
	return short + "-" + hex.EncodeToString(h[:])[:8]
}

func pathHash(p string) string {
	h := sha256.Sum256([]byte(norm(p)))
	return hex.EncodeToString(h[:])[:16]
}

// detector finds fixes of one kind. A detector that has nothing to say, or not
// enough evidence, returns none.
type detector func(ctx context.Context, e *Env) ([]*Fix, error)

var detectors = []detector{detectMCPPrune, detectContextSlim, detectCacheBreaker, detectModelDefault, detectSettings}

// Detect plans every available fix, largest estimated saving first. It writes
// nothing. A detector that fails is reported in errs and does not hide the rest.
func Detect(ctx context.Context, e *Env) (fixes []*Fix, errs []error) {
	for _, d := range detectors {
		fs, err := d(ctx, e)
		if err != nil {
			errs = append(errs, err)
		}
		fixes = append(fixes, fs...)
	}
	for _, f := range fixes {
		f.Label = "estimated"
		if f.Path != "" {
			if err := e.Allowed(f.Path); err != nil {
				f.Blocked = err.Error()
			}
		}
		if f.updated != nil {
			f.Diff = UnifiedDiff(displayName(e, f.Path), f.old, f.updated)
		}
	}
	sort.SliceStable(fixes, func(i, j int) bool {
		if fixes[i].TokensPerSession != fixes[j].TokensPerSession {
			return fixes[i].TokensPerSession > fixes[j].TokensPerSession
		}
		return fixes[i].ID < fixes[j].ID
	})
	return fixes, errs
}

// Find returns the fix with the given id (or nil).
func Find(fixes []*Fix, id string) *Fix {
	for _, f := range fixes {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// displayName shows a path relative to the project or home so diffs read well.
func displayName(e *Env, p string) string {
	for _, base := range []string{e.ProjectDir, e.Home} {
		if base == "" {
			continue
		}
		if rel, err := relIn(base, p); err == nil {
			return rel
		}
	}
	return p
}

// Result is the outcome of one apply request.
type Result struct {
	ID     string        `json:"id"`
	Status string        `json:"status"` // applied, queued, skipped, refused
	Detail string        `json:"detail"`
	Change *setup.Change `json:"change,omitempty"`
}

// Apply applies the fixes named by ids, which the caller must already have the
// user's approval for. If an agent session is active, applicable fixes are
// queued instead and nothing is written. Each fix is re-planned from the files
// as they are now, so two fixes to one file apply cleanly one after the other.
func Apply(ctx context.Context, e *Env, ids []string) ([]Result, error) {
	act := CheckActivity(ctx, e)
	var out []Result
	if act.Active {
		fixes, _ := Detect(ctx, e)
		for _, id := range ids {
			f := Find(fixes, id)
			switch {
			case f == nil:
				out = append(out, Result{ID: id, Status: "skipped", Detail: "no longer applicable"})
			case !f.Applicable():
				out = append(out, Result{ID: id, Status: "refused", Detail: notApplicable(f)})
			default:
				if err := enqueue(e, f); err != nil {
					return out, err
				}
				out = append(out, Result{ID: id, Status: "queued", Detail: act.Reason + " The fix is queued; run `trove fix --apply-queued` when your agent is idle."})
			}
		}
		return out, nil
	}
	for _, id := range ids {
		r, err := applyOne(ctx, e, id)
		out = append(out, r)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func notApplicable(f *Fix) string {
	switch {
	case f.Blocked != "":
		return f.Blocked
	case f.Advisory != "":
		return "Trove cannot make this change itself. " + f.Advisory
	}
	return "nothing to change"
}

// applyOne re-plans id against the files as they are now, then writes it.
func applyOne(ctx context.Context, e *Env, id string) (Result, error) {
	fixes, _ := Detect(ctx, e)
	f := Find(fixes, id)
	if f == nil {
		return Result{ID: id, Status: "skipped", Detail: "no longer applicable: the evidence or the file has changed. Run `trove fix` to see what is available now"}, nil
	}
	if !f.Applicable() {
		return Result{ID: id, Status: "refused", Detail: notApplicable(f)}, nil
	}
	edit := setup.NewFixEdit(f.Kind, f.ID, f.Path, f.Diff, f.Title, f.old, f.updated)
	changes, err := setup.Apply(e.DataDir, []*setup.Edit{edit}, e.now())
	if err != nil {
		return Result{ID: id, Status: "refused", Detail: err.Error()}, fmt.Errorf("%v. Your original file is untouched; run `trove undo` to reverse anything already applied", err)
	}
	r := Result{ID: id, Status: "applied", Detail: "the original is backed up and `trove undo " + id + "` reverses it"}
	if len(changes) == 1 {
		r.Change = &changes[0]
		if err := register(e, f, changes[0]); err != nil {
			r.Detail += ". Note: Trove could not record it for measurement (" + err.Error() + ")"
		}
	}
	return r, nil
}
