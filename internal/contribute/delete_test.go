package contribute

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testID = "0123456789abcdef0123456789abcdef"

type seen struct {
	method, path, body, ua string
	headers                http.Header
}

func deleteRecorder(t *testing.T, status int, reply string) (*httptest.Server, *[]seen) {
	t.Helper()
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, string(b), r.UserAgent(), r.Header.Clone()})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestDeleteSendsOneBareDeleteToTheEndpointAndInstallID(t *testing.T) {
	srv, got := deleteRecorder(t, http.StatusNoContent, "")
	for _, endpoint := range []string{srv.URL + "/v1/contribute", srv.URL + "/v1/contribute/"} {
		*got = nil
		if err := Delete(context.Background(), endpoint, testID); err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
		if len(*got) != 1 {
			t.Fatalf("%s: %d requests, want exactly 1", endpoint, len(*got))
		}
		r := (*got)[0]
		if r.method != http.MethodDelete || r.path != "/v1/contribute/"+testID {
			t.Errorf("sent %s %s", r.method, r.path)
		}
		if r.body != "" {
			t.Errorf("a delete request must have no body, got %q", r.body)
		}
		for _, h := range []string{"Authorization", "Cookie", "X-Api-Key", "Proxy-Authorization"} {
			if r.headers.Get(h) != "" {
				t.Errorf("a delete request must carry no credentials, found %s", h)
			}
		}
		if r.ua != "trove-contribute" {
			t.Errorf("user agent = %q", r.ua)
		}
	}
}

func TestDeleteRefusesWhatItCannotSendSafely(t *testing.T) {
	srv, got := deleteRecorder(t, http.StatusNoContent, "")
	for name, c := range map[string]struct{ endpoint, id string }{
		"empty endpoint":       {"", testID},
		"empty id":             {srv.URL, ""},
		"short id":             {srv.URL, "abc"},
		"id with a slash":      {srv.URL, "../../0123456789abcdef01234567"},
		"upper-case id":        {srv.URL, strings.ToUpper(testID)},
		"id with a query":      {srv.URL, testID[:30] + "?x"},
		"not a valid endpoint": {"ftp://example.com/v1/contribute", testID},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Delete(context.Background(), c.endpoint, c.id); err == nil {
				t.Error("expected an error")
			}
		})
	}
	if len(*got) != 0 {
		t.Errorf("%d request(s) were sent for inputs that must be refused first", len(*got))
	}
}

func TestDeleteFailuresKeepEverythingAndNeverEchoTheReply(t *testing.T) {
	srv, _ := deleteRecorder(t, http.StatusInternalServerError, "SECRET-REPLY-BODY db password hunter2")
	err := Delete(context.Background(), srv.URL, testID)
	if err == nil {
		t.Fatal("a 500 must be an error")
	}
	for _, bad := range []string{"SECRET-REPLY-BODY", "hunter2", srv.URL, testID} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("the error leaks %q: %v", bad, err)
		}
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "install id is kept") {
		t.Errorf("the error should say what happened and that nothing was lost: %v", err)
	}
}

func TestDeleteFollowsNoRedirects(t *testing.T) {
	var elsewhere int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&elsewhere, 1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/captured", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if err := Delete(context.Background(), srv.URL, testID); err == nil {
		t.Error("a redirect is not a success")
	}
	if atomic.LoadInt32(&elsewhere) != 0 {
		t.Error("the request was followed to another host")
	}
}

func TestDeleteGivesUpWithinItsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Delete(ctx, srv.URL, testID)
	if err == nil || time.Since(start) > Timeout {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
	if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "install id is kept") {
		t.Errorf("unexpected message: %v", err)
	}
}
