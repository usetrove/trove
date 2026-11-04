package external

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/redact"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

// Runner executes the manifest's install, remove and verify commands. The
// real one runs argv directly (never through a shell); tests use a fake.
type Runner interface {
	Run(ctx context.Context, argv []string) (output string, err error)
	LookPath(name string) (string, error)
}

// Store records installed tools. *store.ExternalTools satisfies it.
type Store interface {
	Put(ctx context.Context, t store.ExternalTool) error
	Get(ctx context.Context, name string) (store.ExternalTool, bool, error)
	List(ctx context.Context) ([]store.ExternalTool, error)
	SetState(ctx context.Context, name, state string) (bool, error)
	Delete(ctx context.Context, name string) error
}

// Env is everything the lifecycle needs from the outside world.
type Env struct {
	DataDir   string
	ClaudeDir string // directory holding the agent's user-level rules file
	Runner    Runner
	Store     Store
	Now       func() time.Time
	// BuiltinsOn lists built-in optimizers whose state is "on" (for conflict checks).
	BuiltinsOn func() []string
	Manifests  []Manifest
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Approval is the user's explicit consent, collected by the CLI. Trove
// never builds one itself.
type Approval struct {
	Tool  string // the tool the user approved
	Typed string // what the user typed when the license needed typed confirmation
}

// InstallPlan is everything shown to the user before anything runs.
type InstallPlan struct {
	Manifest        Manifest      `json:"manifest"`
	License         LicenseNotice `json:"license"`
	InstallArgv     []string      `json:"install_argv,omitempty"`
	MissingRequires []string      `json:"missing_requires,omitempty"`
	OverheadTokens  int           `json:"overhead_tokens_per_request"`
	AlreadyHere     bool          `json:"already_installed"`
	ShadowNote      string        `json:"shadow_note"`
}

// Find returns the manifest named name.
func (e Env) Find(name string) (Manifest, error) {
	for _, m := range e.Manifests {
		if m.Name == name {
			return m, nil
		}
	}
	return Manifest{}, fmt.Errorf("Trove has no external tool called %q. Run `trove external list` to see the available ones", name)
}

// PlanInstall describes what install would do. It changes nothing.
func (e Env) PlanInstall(ctx context.Context, name string) (InstallPlan, error) {
	m, err := e.Find(name)
	if err != nil {
		return InstallPlan{}, err
	}
	p := InstallPlan{Manifest: m, License: NoticeFor(m), InstallArgv: m.Install.Argv, OverheadTokens: ChargeFor(m).PerRequest}
	for _, r := range m.Install.Requires {
		if _, err := e.Runner.LookPath(r); err != nil {
			p.MissingRequires = append(p.MissingRequires, r)
		}
	}
	if _, ok, err := e.Store.Get(ctx, name); err != nil {
		return p, err
	} else if ok {
		p.AlreadyHere = true
	}
	if !m.SupportsShadow {
		p.ShadowNote = ShadowRefusal(m)
	}
	return p, nil
}

// Install runs the manifest's install command after approval, verifies the
// pinned version, and records the tool as installed but off.
func (e Env) Install(ctx context.Context, name string, ap Approval) error {
	plan, err := e.PlanInstall(ctx, name)
	if err != nil {
		return err
	}
	m := plan.Manifest
	if ap.Tool != name {
		return fmt.Errorf("no approval for %s was given, so nothing was installed", name)
	}
	if plan.License.NeedsTypedConfirm && ap.Typed != name {
		return fmt.Errorf("%s has a license that needs a closer look. Type its name (%s) to confirm you have read it. Nothing was installed", name, name)
	}
	if plan.AlreadyHere {
		return fmt.Errorf("%s is already installed. Run `trove external remove %s` first if you want to reinstall", name, name)
	}
	if len(plan.MissingRequires) > 0 {
		return fmt.Errorf("%s needs %s on your PATH first. Install that, then run this again. Nothing was changed", name, strings.Join(plan.MissingRequires, ", "))
	}
	if len(m.Install.Argv) > 0 {
		if out, err := e.Runner.Run(ctx, m.Install.Argv); err != nil {
			return fmt.Errorf("the install command for %s failed: %v%s. Nothing was recorded; fix the problem and run this again", name, err, tail(out))
		}
	}
	if len(m.Verify.Argv) > 0 {
		out, err := e.Runner.Run(ctx, m.Verify.Argv)
		if err != nil || !strings.Contains(out, m.Verify.Contains) {
			return fmt.Errorf("%s was installed but did not report version %s, so Trove did not record it. Run `trove external remove %s` to clean up, or check the tool by hand", name, m.Version, name)
		}
	}
	now := e.now()
	return e.Store.Put(ctx, store.ExternalTool{Name: name, Version: m.Version, License: m.License,
		State: "off", InstalledAt: now, LicenseAckAt: now})
}

// Wiring plans the config edits that make a tool active for new sessions.
type Wiring func(e Env, m Manifest) ([]*setup.Edit, error)

var wirings = map[ActsAs]Wiring{ActsRulesFile: wireRules, ActsHook: wireHook}

// RegisterWiring lets an adapter supply the edits for an ActsAs kind.
func RegisterWiring(a ActsAs, w Wiring) { wirings[a] = w }

func wireHook(e Env, m Manifest) ([]*setup.Edit, error) {
	if e.ClaudeDir == "" {
		return nil, errors.New("Trove could not find your agent's config folder, so it cannot add the hook. Set it up with the agent first")
	}
	ed, err := setup.PlanExternalHook(m.Name, filepath.Join(e.ClaudeDir, "settings.json"),
		setup.HookSpec{Event: m.Wire.HookEvent, Matcher: m.Wire.HookMatcher, Command: m.Wire.HookCommand})
	if err != nil {
		return nil, err
	}
	return []*setup.Edit{ed}, nil
}

func wireRules(e Env, m Manifest) ([]*setup.Edit, error) {
	if e.ClaudeDir == "" {
		return nil, errors.New("Trove could not find your agent's config folder, so it cannot add the rules. Set it up with the agent first")
	}
	path := filepath.Join(e.ClaudeDir, "CLAUDE.md")
	old, err := os.ReadFile(path) //nolint:gosec // user's own agent config
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		old = nil
	}
	open, close := setup.ExternalMarkers(m.Name)
	block := open + "\n" + strings.TrimRight(m.Rules, "\n") + "\n" + close + "\n"
	if strings.Contains(string(old), open) {
		return []*setup.Edit{setup.NewExternalEdit(m.Name, path, block, "already present", open, old, old)}, nil
	}
	updated := string(old)
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	if updated != "" {
		updated += "\n"
	}
	updated += block
	return []*setup.Edit{setup.NewExternalEdit(m.Name, path, block,
		"Adds "+m.Name+"'s rules to your agent's CLAUDE.md, fenced by markers so they can be removed exactly.", open, old, []byte(updated))}, nil
}

// EnablePlan is everything enabling a tool would do. Nothing has happened yet.
type EnablePlan struct {
	Manifest  Manifest
	Conflicts []Conflict
	Edits     []*setup.Edit
	// Commands are run through the agent's own CLI (MCP-server tools).
	Commands [][]string
	// AlreadyOn is true when the tool is already enabled.
	AlreadyOn bool
}

// PlanEnable returns the conflicts, file edits and commands enabling name would cause.
func (e Env) PlanEnable(ctx context.Context, name string) (EnablePlan, error) {
	var p EnablePlan
	m, err := e.Find(name)
	if err != nil {
		return p, err
	}
	p.Manifest = m
	rec, ok, err := e.Store.Get(ctx, name)
	if err != nil {
		return p, err
	}
	if !ok {
		return p, fmt.Errorf("%s is not installed. Run `trove external install %s` first", name, name)
	}
	all, err := e.Store.List(ctx)
	if err != nil {
		return p, err
	}
	var enabled []Manifest
	for _, t := range all {
		if t.State == "on" {
			if om, err := e.Find(t.Name); err == nil {
				enabled = append(enabled, om)
			}
		}
	}
	var builtins []string
	if e.BuiltinsOn != nil {
		builtins = e.BuiltinsOn()
	}
	p.Conflicts = CheckEnable(m, enabled, builtins)
	if rec.State == "on" {
		p.AlreadyOn = true
		return p, nil
	}
	if m.ActsAs == ActsMCPServer {
		if _, err := e.Runner.LookPath(m.Wire.Add[0]); err != nil {
			return p, fmt.Errorf("%s needs %s on your PATH to register the tool with your agent. Install it, then run this again. Nothing was changed", name, m.Wire.Add[0])
		}
		p.Commands = [][]string{m.Wire.Add}
		return p, nil
	}
	w, ok := wirings[m.ActsAs]
	if !ok {
		return p, fmt.Errorf("Trove cannot switch %s on yet: wiring for %s tools is not available in this version", name, m.ActsAs)
	}
	p.Edits, err = w(e, m)
	return p, err
}

// Enable applies the plan and marks the tool on. It refuses on conflicts. The
// change takes effect for sessions started afterwards.
func (e Env) Enable(ctx context.Context, name string) ([]setup.Change, error) {
	p, err := e.PlanEnable(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(p.Conflicts) > 0 {
		return nil, p.Conflicts[0]
	}
	if p.AlreadyOn {
		return nil, nil
	}
	rec, _, err := e.Store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, argv := range p.Commands {
		if out, err := e.Runner.Run(ctx, argv); err != nil {
			return nil, fmt.Errorf("registering %s with your agent failed: %v%s. Nothing was recorded as on", name, err, tail(out))
		}
	}
	changes, err := setup.Apply(e.DataDir, p.Edits, e.now())
	rec.ChangeIDs = append(rec.ChangeIDs, changeIDs(changes)...)
	rec.State = "on"
	if perr := e.Store.Put(ctx, rec); perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		return changes, fmt.Errorf("could not fully enable %s: %v. Run `trove external disable %s` to undo what was written", name, err, name)
	}
	return changes, nil
}

// Disable undoes the tool's recorded config changes and marks it off. The
// tool itself stays installed.
func (e Env) Disable(ctx context.Context, name string) ([]setup.UndoResult, error) {
	rec, ok, err := e.Store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s is not installed, so there is nothing to disable", name)
	}
	var res []setup.UndoResult
	if m, err := e.Find(name); err == nil && m.ActsAs == ActsMCPServer && rec.State == "on" {
		if out, err := e.Runner.Run(ctx, m.Wire.Remove); err != nil {
			return nil, fmt.Errorf("unregistering %s from your agent failed: %v%s. It stays on; fix that and run this again", name, err, tail(out))
		}
	}
	if len(rec.ChangeIDs) > 0 {
		cs, err := setup.LoadChanges(e.DataDir)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{}
		for _, c := range cs {
			have[c.ID] = true
		}
		var ids []string
		for _, id := range rec.ChangeIDs {
			if have[id] { // already reversed by `trove undo`
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			res, err = setup.UndoSelected(e.DataDir, ids)
			if err != nil {
				return res, err
			}
		}
		for _, r := range res {
			if r.Action == "left_alone" {
				return res, fmt.Errorf("%s: %s", r.Path, r.Detail)
			}
		}
	}
	rec.ChangeIDs = nil
	rec.State = "off"
	return res, e.Store.Put(ctx, rec)
}

// Remove disables the tool, runs its remove command and forgets it.
func (e Env) Remove(ctx context.Context, name string, ap Approval) error {
	m, err := e.Find(name)
	if err != nil {
		return err
	}
	if ap.Tool != name {
		return fmt.Errorf("no approval for removing %s was given, so nothing was removed", name)
	}
	if _, ok, err := e.Store.Get(ctx, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s is not installed", name)
	}
	if _, err := e.Disable(ctx, name); err != nil {
		return fmt.Errorf("could not undo %s's config changes: %v. It stays installed; fix that and run this again", name, err)
	}
	if len(m.Remove.Argv) > 0 {
		if out, err := e.Runner.Run(ctx, m.Remove.Argv); err != nil {
			return fmt.Errorf("the remove command for %s failed: %v%s. Its config changes are already undone and it is still recorded as installed; remove it by hand, then run this again", name, err, tail(out))
		}
	}
	return e.Store.Delete(ctx, name)
}

func changeIDs(cs []setup.Change) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// tail returns a short, redacted piece of command output for error messages.
func tail(out string) string {
	out = strings.TrimSpace(redact.String(out))
	if out == "" {
		return ""
	}
	if len(out) > 300 {
		out = "…" + out[len(out)-300:]
	}
	return " (" + out + ")"
}
