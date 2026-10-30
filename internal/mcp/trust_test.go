package mcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// allowAll approves everything. The older run tests are about running, not about
// trust, so they use it.
type allowAll struct{}

func (allowAll) Approved(string, string) bool { return true }

func TestRunRefusesUnapprovedCommandsFile(t *testing.T) {
	r := runProject(t, map[string][]string{"test": {"pass"}}, nil)
	blob := &memBlob{}
	for name, trust := range map[string]Trust{
		"nil trust":      nil,
		"empty store":    OpenTrust(t.TempDir()),
		"approves wrong": fixedTrust{hash: "not-the-hash"},
	} {
		got, err := Run(context.Background(), r, blob, trust, RunArgs{Name: "test"})
		if err == nil || !strings.Contains(err.Error(), "not been approved") {
			t.Errorf("%s: want a refusal, got %q, %v", name, got, err)
		}
		if len(blob.data) != 0 {
			t.Fatalf("%s: the command ran (output was stored)", name)
		}
	}
}

type fixedTrust struct{ hash string }

func (f fixedTrust) Approved(_, h string) bool { return h == f.hash }

func TestApprovalRunsAndEditWithdrawsIt(t *testing.T) {
	r := runProject(t, map[string][]string{"test": {"pass"}}, nil)
	_, hash, err := PendingCommands(r)
	if err != nil || hash == "" {
		t.Fatalf("hash %q, %v", hash, err)
	}
	ts := OpenTrust(t.TempDir())
	if err := ts.Approve(r.Dir(), hash); err != nil {
		t.Fatal(err)
	}
	got, err := Run(context.Background(), r, &memBlob{}, ts, RunArgs{Name: "test"})
	if err != nil || !strings.HasPrefix(got, "test: PASS") {
		t.Fatalf("approved file should run: %v\n%s", err, got)
	}

	// A hostile edit after approval (a git pull, say) must not inherit it.
	p := filepath.Join(r.Dir(), ".trove", "commands.toml")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(b, []byte("\n[commands.evil]\nrun = [\"sh\", \"-c\", \"id\"]\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	blob := &memBlob{}
	if _, err := Run(context.Background(), r, blob, ts, RunArgs{Name: "evil"}); err == nil || !strings.Contains(err.Error(), "not been approved") {
		t.Fatalf("edited file ran: %v", err)
	}
	if len(blob.data) != 0 {
		t.Fatal("edited file executed a command")
	}

	if err := ts.Revoke(r.Dir()); err != nil {
		t.Fatal(err)
	}
}

func TestTrustIsPerProjectAndStoresNoPaths(t *testing.T) {
	dir := t.TempDir()
	ts := OpenTrust(dir)
	if err := ts.Approve("/home/me/work/app", "h1"); err != nil {
		t.Fatal(err)
	}
	if ts.Approved("/home/me/work/other", "h1") {
		t.Error("approval leaked to another project")
	}
	if !ts.Approved("/home/me/work/app", "h1") {
		t.Error("approval lost")
	}
	b, err := os.ReadFile(filepath.Join(dir, trustFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "work") || strings.Contains(string(b), "/home") {
		t.Errorf("trust file holds a path: %s", b)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, trustFile))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("trust file mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestDamagedTrustFileApprovesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, trustFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if OpenTrust(dir).Approved("/p", "h") {
		t.Error("a damaged trust file must fail closed")
	}
}
