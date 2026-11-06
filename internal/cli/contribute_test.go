package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func contributeHarness(t *testing.T) (h *harness, dir string, hits *int32, last *string) {
	t.Helper()
	hits, last = new(int32), new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		b, _ := io.ReadAll(r.Body)
		*last = string(b)
	}))
	t.Cleanup(srv.Close)
	h = newHarness(t, nil)
	dir = h.app.Getenv("TROVE_DATA_DIR")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[contribute]\nendpoint = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, dir, hits, last
}

func (h *harness) reset() { h.out.Reset(); h.err.Reset() }

func TestContributeSendsNothingUntilEnabled(t *testing.T) {
	h, _, hits, _ := contributeHarness(t)
	for _, args := range [][]string{{"report", "--no-history"}, {"contribute", "--no-history"}, {"contribute", "status"}} {
		h.reset()
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, h.err)
		}
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("server was contacted %d times before consent", n)
	}
}

func TestContributeEnableReportDisable(t *testing.T) {
	h, dir, hits, last := contributeHarness(t)

	// Without a yes the preview is shown and nothing is turned on.
	if code := h.run("contribute", "enable", "--no-history"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "exactly this text") || !strings.Contains(h.out.String(), "Nothing changed") {
		t.Errorf("enable without consent:\n%s", h.out)
	}
	if _, err := os.Stat(filepath.Join(dir, "contribute.json")); err == nil {
		t.Fatal("consent record written without a yes")
	}

	h.reset()
	if code := h.run("contribute", "enable", "--yes", "--no-history"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("enable itself must not upload")
	}

	// A report with an explicit period is never shared.
	h.reset()
	h.run("report", "--today", "--no-history")
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("a --today report was shared")
	}

	// The default report shares once, then waits a week.
	h.reset()
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("hits = %d, want 1", atomic.LoadInt32(hits))
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(*last), &sent); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, *last)
	}
	if id, _ := sent["install_id"].(string); len(id) != 32 {
		t.Errorf("install_id = %v", sent["install_id"])
	}
	h.reset()
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("shared twice within a week: %d hits", atomic.LoadInt32(hits))
	}

	// The preview shows what was sent, byte for byte.
	h.reset()
	h.run("contribute", "--no-history")
	if !strings.Contains(h.out.String(), strings.TrimSpace(*last)) {
		t.Errorf("preview differs from the upload:\n%s\nvs\n%s", h.out, *last)
	}

	// Disable stops it, even after a reset makes an upload due again.
	h.reset()
	if code := h.run("contribute", "disable"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	h.reset()
	h.run("contribute", "reset")
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("uploaded after disable: %d hits", atomic.LoadInt32(hits))
	}
}

func TestContributeEnableNeedsAnEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[contribute]\nendpoint = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("contribute", "enable", "--yes", "--no-history"); code == 0 {
		t.Fatal("enable with the endpoint blanked should fail")
	}
	if !strings.Contains(h.err.String(), "endpoint") {
		t.Errorf("error should say what to do: %s", h.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "contribute.json")); err == nil {
		t.Error("consent record written although there is nowhere to send")
	}
}

// Shipping a default endpoint must not send anything by itself: without a "yes"
// there is no consent record, so nothing can be uploaded.
func TestDefaultEndpointStillNeedsConsent(t *testing.T) {
	h := newHarness(t, nil)
	dir := h.app.Getenv("TROVE_DATA_DIR")
	if code := h.run("contribute", "status", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var st contributeStatus
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Endpoint != "https://contribute.troverelay.com/v1/contribute" {
		t.Errorf("status = %+v, want off with the built-in endpoint", st)
	}
	h.reset()
	if code := h.run("contribute", "enable", "--no-history"); code != 0 { // no --yes, no terminal input
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "Nothing changed") {
		t.Errorf("enable without a yes must change nothing:\n%s", h.out)
	}
	if _, err := os.Stat(filepath.Join(dir, "contribute.json")); err == nil {
		t.Error("consent record written without a yes")
	}
}

// Consent is informed: the exact payload and the address it goes to are printed before anything is recorded,
// with or without --yes, and the printed example is the same text `trove contribute` shows.
func TestEnableShowsTheExamplePayloadAndAddressBeforeConsent(t *testing.T) {
	for _, args := range [][]string{{"contribute", "enable", "--no-history"}, {"contribute", "enable", "--yes", "--no-history"}} {
		h, dir, hits, _ := contributeHarness(t)
		cfgBytes, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
		endpoint := strings.Trim(strings.TrimSpace(strings.SplitN(string(cfgBytes), "endpoint = ", 2)[1]), `"`)
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, h.err)
		}
		out := h.out.String()
		for _, want := range []string{endpoint, `"install_id"`, `"schema_version"`, `"requests_bucket"`, "at most once every 7 days"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: the consent screen does not show %q:\n%s", args, want, out)
			}
		}
		if i, j := strings.Index(out, `"install_id"`), strings.Index(out, "Nothing changed"); !strings.Contains(strings.Join(args, " "), "--yes") && (j < 0 || i > j) {
			t.Errorf("%v: the payload must come before the outcome:\n%s", args, out)
		}
		if atomic.LoadInt32(hits) != 0 {
			t.Errorf("%v: enabling must never upload", args)
		}
	}
}

// ---- trove contribute delete (CLAUDE.md rule 1, part c) ----

type reqLog struct {
	method, path, body string
}

// receiver is a stand-in for the summary receiver that records every request it gets.
func receiver(t *testing.T, deleteStatus int) (h *harness, dir string, got *[]reqLog) {
	t.Helper()
	got = new([]reqLog)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = append(*got, reqLog{r.Method, r.URL.Path, string(b)})
		if r.Method == http.MethodDelete {
			w.WriteHeader(deleteStatus)
		}
	}))
	t.Cleanup(srv.Close)
	h = newHarness(t, nil)
	dir = h.app.Getenv("TROVE_DATA_DIR")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[contribute]\nendpoint = \""+srv.URL+"/v1/contribute\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, dir, got
}

func deletes(got []reqLog) (n int) {
	for _, r := range got {
		if r.method == http.MethodDelete {
			n++
		}
	}
	return n
}

func installID(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "contribute.json"))
	if err != nil {
		return ""
	}
	var st struct {
		Enabled   bool   `json:"enabled"`
		InstallID string `json:"install_id"`
	}
	_ = json.Unmarshal(b, &st)
	return st.InstallID
}

func TestContributeDeleteWithNoInstallIDSendsNothing(t *testing.T) {
	h, _, got := receiver(t, 204)
	if code := h.run("contribute", "delete", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if len(*got) != 0 || !strings.Contains(h.out.String(), "Nothing to delete") {
		t.Errorf("requests=%v out=%s", *got, h.out)
	}
}

func TestContributeDeleteAsksFirstAndSendsNothingWithoutAYes(t *testing.T) {
	h, dir, got := receiver(t, 204)
	h.run("contribute", "enable", "--yes", "--no-history")
	id := installID(t, dir)
	h.reset()
	// No terminal and no --yes: the request is shown, not sent.
	if code := h.run("contribute", "delete"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "DELETE ") || !strings.Contains(h.out.String(), "/v1/contribute/"+id) || !strings.Contains(h.out.String(), "Nothing was sent") {
		t.Errorf("the request must be shown before anything is sent:\n%s", h.out)
	}
	if deletes(*got) != 0 || installID(t, dir) != id {
		t.Errorf("a refused delete changed something: %v", *got)
	}
}

func TestContributeDeleteSendsOneBareRequestThenTurnsSharingOffAndForgetsTheID(t *testing.T) {
	h, dir, got := receiver(t, 204)
	h.run("contribute", "enable", "--yes", "--no-history")
	id := installID(t, dir)
	h.reset()
	if code := h.run("contribute", "delete", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.out, h.err)
	}
	if !strings.Contains(h.out.String(), "Sharing is now off") {
		t.Errorf("the result should say sharing is off, got: %s", h.out)
	}
	if deletes(*got) != 1 {
		t.Fatalf("requests = %v, want exactly one DELETE", *got)
	}
	last := (*got)[len(*got)-1]
	if last.method != http.MethodDelete || last.path != "/v1/contribute/"+id || last.body != "" {
		t.Errorf("sent %+v", last)
	}
	if installID(t, dir) != "" {
		t.Error("the local install id must be removed after the receiver accepted the request")
	}
	// Sharing is off for good: a report no longer uploads anything.
	before := len(*got)
	h.run("report", "--no-history")
	if len(*got) != before {
		t.Errorf("a report uploaded after the data was deleted: %v", (*got)[before:])
	}
}

func TestContributeDeleteFailureChangesNothingSoItCanBeRetried(t *testing.T) {
	h, dir, got := receiver(t, 500)
	h.run("contribute", "enable", "--yes", "--no-history")
	id := installID(t, dir)
	h.reset()
	if code := h.run("contribute", "delete", "--yes"); code == 0 {
		t.Fatal("a refused request must be reported as a failure")
	}
	if deletes(*got) != 1 {
		t.Errorf("requests = %v", *got)
	}
	if installID(t, dir) != id {
		t.Error("the install id must survive a failed delete, or the user could never retry")
	}
	if !strings.Contains(h.err.String(), "HTTP 500") {
		t.Errorf("stderr should say what happened: %q", h.err)
	}
}

// The delete request has exactly one trigger. Every other command, including the ones that remove local data and
// the ones that upload, must never send it.
func TestNothingButContributeDeleteEverSendsADelete(t *testing.T) {
	h, dir, got := receiver(t, 204)
	h.run("contribute", "enable", "--yes", "--no-history")
	for _, args := range [][]string{
		{"report", "--no-history"}, {"contribute"}, {"contribute", "status"}, {"contribute", "disable"}, {"contribute", "reset"},
		{"data", "export", "--out", filepath.Join(t.TempDir(), "x")}, {"data", "delete", "--yes"},
		{"uninstall", "--yes", "--purge-data"}, {"doctor"},
	} {
		h.reset()
		h.run(args...)
		if deletes(*got) != 0 {
			t.Fatalf("`trove %s` sent a DELETE: %v", strings.Join(args, " "), *got)
		}
	}
	_ = dir
}
