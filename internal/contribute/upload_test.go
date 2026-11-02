package contribute

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type recorder struct {
	hits int32
	body string
	hdr  http.Header
	code int
}

func (r *recorder) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		atomic.AddInt32(&r.hits, 1)
		b, _ := io.ReadAll(q.Body)
		r.body, r.hdr = string(b), q.Header.Clone()
		if r.code != 0 {
			w.WriteHeader(r.code)
		}
	}))
}

func enabledDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := SaveState(dir, State{Enabled: true, InstallID: "abc123"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAutoSendsExactlyTheEncodedPayloadWithNoCredentials(t *testing.T) {
	rec := &recorder{}
	srv := rec.server()
	defer srv.Close()
	dir := enabledDir(t)
	p := Build(sample(), Meta{TroveVersion: "1.2.3", OS: "linux", Arch: "amd64"})
	sent, err := Auto(context.Background(), dir, srv.URL, p, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	p.InstallID = "abc123"
	want, _ := Encode(p)
	if rec.body != string(want) {
		t.Errorf("body differs from what preview shows:\n got %s\nwant %s", rec.body, want)
	}
	for _, h := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if rec.hdr.Get(h) != "" {
			t.Errorf("header %s must not be sent", h)
		}
	}
	st, _ := LoadState(dir)
	if st.LastSent != "2026-11-01" {
		t.Errorf("last_sent = %q", st.LastSent)
	}
}

func TestAutoDoesNothingUnlessConsentedAndDue(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		state *State
		want  bool
	}{
		{"no consent file", nil, false},
		{"consent off", &State{Enabled: false, InstallID: "x"}, false},
		{"enabled but no id", &State{Enabled: true}, false},
		{"sent 3 days ago", &State{Enabled: true, InstallID: "x", LastSent: "2026-10-29"}, false},
		{"sent 8 days ago", &State{Enabled: true, InstallID: "x", LastSent: "2026-10-24"}, true},
		{"never sent", &State{Enabled: true, InstallID: "x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server()
			defer srv.Close()
			dir := t.TempDir()
			if c.state != nil {
				if err := SaveState(dir, *c.state); err != nil {
					t.Fatal(err)
				}
			}
			sent, err := Auto(context.Background(), dir, srv.URL, Build(sample(), Meta{}), now)
			if err != nil || sent != c.want {
				t.Errorf("sent=%v err=%v, want sent=%v", sent, err, c.want)
			}
			if got := atomic.LoadInt32(&rec.hits) > 0; got != c.want {
				t.Errorf("server contacted = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAutoWithNoEndpointNeverConnects(t *testing.T) {
	sent, err := Auto(context.Background(), enabledDir(t), "", Build(sample(), Meta{}), time.Now())
	if sent || err != nil {
		t.Errorf("sent=%v err=%v", sent, err)
	}
}

func TestFailureKeepsStateSoItRetriesAndHidesTheReply(t *testing.T) {
	rec := &recorder{code: 500}
	srv := rec.server()
	defer srv.Close()
	dir := enabledDir(t)
	sent, err := Auto(context.Background(), dir, srv.URL, Build(sample(), Meta{}), time.Now())
	if sent || err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if st, _ := LoadState(dir); st.LastSent != "" {
		t.Errorf("last_sent must stay empty after a failure, got %q", st.LastSent)
	}
}

func TestUnreachableEndpointErrorHasNoURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	err := Send(context.Background(), url, []byte("{}"))
	if err == nil || strings.Contains(err.Error(), url) {
		t.Errorf("err = %v", err)
	}
}

func TestSendRefusesUnsafeEndpoints(t *testing.T) {
	for _, ep := range []string{"", "http://example.com/x", "https://user:pw@example.com/x", "https://example.com/x?token=1", "ftp://example.com"} {
		if err := Send(context.Background(), ep, []byte("{}")); err == nil {
			t.Errorf("%q should be refused", ep)
		}
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	target := &recorder{}
	ts := target.server()
	defer ts.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ts.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	if err := Send(context.Background(), redir.URL, []byte("{}")); err == nil {
		t.Error("a redirect is not a success")
	}
	if atomic.LoadInt32(&target.hits) != 0 {
		t.Error("the redirect target was contacted")
	}
}

func TestDamagedStateNeverTurnsSharingOn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte(`{"enabled": tru`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(dir)
	if err == nil || st.Enabled {
		t.Errorf("state=%+v err=%v", st, err)
	}
}

func TestSaveStateIsOwnerOnlyAndInstallIDIsRandom(t *testing.T) {
	dir := t.TempDir()
	if err := SaveState(dir, State{Enabled: true, InstallID: "a"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m&0o077 != 0 && !isWindows() {
		t.Errorf("mode = %v, want owner only", m)
	}
	a, _ := NewInstallID()
	b, _ := NewInstallID()
	if len(a) != 32 || a == b {
		t.Errorf("ids %q %q", a, b)
	}
}
