package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func writeStr(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const existingCodex = "# my settings\nmodel = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"npx\"\nargs = [\"-y\", \"thing\"]\n"

func TestPlanCodexMCP(t *testing.T) {
	tests := []struct {
		name, existing string
		wantErr        string
		wantNoOp       bool
	}{
		{name: "new file"},
		{name: "existing file", existing: existingCodex},
		{name: "no trailing newline", existing: "model = \"x\""},
		{name: "already defined by the user", existing: "[mcp_servers.trove]\ncommand = \"mine\"\n", wantErr: "already defines"},
		{name: "invalid toml", existing: "[oops", wantErr: "not valid TOML"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.existing != "" {
				writeStr(t, filepath.Join(dir, "config.toml"), tc.existing)
			}
			e, err := PlanCodexMCP(dir, `C:\Program Files\trove\trove.exe`)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := string(e.Updated())
			if !strings.HasPrefix(got, tc.existing) {
				t.Errorf("the user's content must be kept as is:\n%s", got)
			}
			var parsed struct {
				MCP map[string]struct {
					Command string   `toml:"command"`
					Args    []string `toml:"args"`
				} `toml:"mcp_servers"`
			}
			if err := toml.Unmarshal(e.Updated(), &parsed); err != nil {
				t.Fatalf("result is not valid TOML: %v\n%s", err, got)
			}
			if s := parsed.MCP["trove"]; s.Command != `C:\Program Files\trove\trove.exe` || len(s.Args) != 1 || s.Args[0] != "mcp" {
				t.Errorf("wrong entry: %+v", s)
			}
		})
	}
}

func TestCodexMCPApplyUndoExact(t *testing.T) {
	data, home := t.TempDir(), t.TempDir()
	cfg := filepath.Join(home, "config.toml")
	writeStr(t, cfg, existingCodex)
	e, err := PlanCodexMCP(home, "trove")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := Apply(data, []*Edit{e}, time.Now())
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %v", cs, err)
	}
	if !strings.Contains(readStr(t, cfg), "[mcp_servers.trove]") {
		t.Fatal("not applied")
	}
	// Applying again plans nothing.
	if e2, _ := PlanCodexMCP(home, "trove"); !e2.NoOp {
		t.Error("second plan should be a no-op")
	}
	res, err := Undo(data)
	if err != nil || len(res) != 1 || res[0].Action != "restored" {
		t.Fatalf("%+v %v", res, err)
	}
	if readStr(t, cfg) != existingCodex {
		t.Error("undo must restore the original byte for byte")
	}
}

func TestCodexMCPUndoAfterUserEditKeepsTheirEdit(t *testing.T) {
	data, home := t.TempDir(), t.TempDir()
	cfg := filepath.Join(home, "config.toml")
	writeStr(t, cfg, existingCodex)
	e, _ := PlanCodexMCP(home, "trove")
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	writeStr(t, cfg, readStr(t, cfg)+"\n# added later\napproval_policy = \"never\"\n")
	res, err := Undo(data)
	if err != nil || len(res) != 1 || res[0].Action != "removed_our_lines" {
		t.Fatalf("%+v %v", res, err)
	}
	got := readStr(t, cfg)
	if strings.Contains(got, "mcp_servers.trove") || !strings.Contains(got, "approval_policy") || !strings.Contains(got, "[mcp_servers.other]") {
		t.Errorf("only Trove's block should go:\n%s", got)
	}
}

func TestMCPAndGatewayUndoDoNotTouchEachOther(t *testing.T) {
	data, home := t.TempDir(), t.TempDir()
	cfg := filepath.Join(home, "config.toml")
	gw, err := PlanCodex(home, 8787)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(data, []*Edit{gw}, time.Now()); err != nil {
		t.Fatal(err)
	}
	mc, err := PlanCodexMCP(home, "trove")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(data, []*Edit{mc}, time.Now()); err != nil {
		t.Fatal(err)
	}
	writeStr(t, cfg, readStr(t, cfg)+"\n# user edit\n") // forces the surgical path
	cs, _ := LoadChanges(data)
	var mcpID string
	for _, c := range cs {
		if c.Agent == CodexMCP {
			mcpID = c.ID
		}
	}
	if _, err := UndoSelected(data, []string{mcpID}); err != nil {
		t.Fatal(err)
	}
	got := readStr(t, cfg)
	if strings.Contains(got, "mcp_servers.trove") || !strings.Contains(got, "model_providers.trove") {
		t.Errorf("undoing the MCP change must keep the gateway change:\n%s", got)
	}
}

func TestPlanProjectMCP(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	other := `{"mcpServers":{"db":{"command":"npx","args":["-y","dbhub"]}},"extra":1}`
	writeStr(t, p, other)
	e, err := PlanProjectMCP(dir, "trove")
	if err != nil {
		t.Fatal(err)
	}
	got := string(e.Updated())
	for _, want := range []string{`"db"`, `"dbhub"`, `"extra"`, `"trove"`, `"mcp"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s:\n%s", want, got)
		}
	}
	data := t.TempDir()
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e2, _ := PlanProjectMCP(dir, "trove"); !e2.NoOp {
		t.Error("should be a no-op once installed")
	}
	if res, err := Undo(data); err != nil || res[0].Action != "restored" || readStr(t, p) != other {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestProjectMCPCreatedFileRemovedAndUserEditKept(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
	e, _ := PlanProjectMCP(dir, "trove")
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, err := Undo(data); err != nil || res[0].Action != "removed_file" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Error("a file Trove created should be removed")
	}
	// Edited since: only the trove entry goes.
	e, _ = PlanProjectMCP(dir, "trove")
	if _, err := Apply(data, []*Edit{e}, time.Now()); err != nil {
		t.Fatal(err)
	}
	writeStr(t, filepath.Join(dir, ".mcp.json"), strings.Replace(readStr(t, filepath.Join(dir, ".mcp.json")), `"mcpServers"`, `"mcpServers": {"db": {"command":"x"}, "trove2": {"command":"y"}}, "old"`, 1))
	if _, err := Undo(data); err != nil {
		t.Fatal(err)
	}
}

func TestProjectMCPRefusals(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	for name, tc := range map[string]struct{ body, want string }{
		"invalid":    {`{oops`, "not valid JSON"},
		"not object": {`{"mcpServers": []}`, "not an object"},
		"taken":      {`{"mcpServers":{"trove":{"command":"mine"}}}`, "already defines"},
	} {
		writeStr(t, p, tc.body)
		if _, err := PlanProjectMCP(dir, "trove"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
}
