package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchAndSummarize(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[
		 {"tag_name":"v0.1.0","assets":[
		  {"name":"trove_linux_amd64.tar.gz","download_count":5},
		  {"name":"trove_darwin_arm64.tar.gz","download_count":3},
		  {"name":"checksums.txt","download_count":99},
		  {"name":"trove_linux_amd64.tar.gz.sbom.json","download_count":40}]},
		 {"tag_name":"v0.2.0-draft","draft":true,"assets":[{"name":"x.tar.gz","download_count":1000}]}]`))
	}))
	defer srv.Close()

	rels, err := fetch(srv.URL, "o/r", "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	s := summarize(rels)
	if len(s) != 1 || s[0].Total != 8 {
		t.Fatalf("want one release totalling 8 (archives only, drafts skipped), got %+v", s)
	}
	var buf bytes.Buffer
	printReport(&buf, rels)
	out := buf.String()
	if strings.Contains(out, "secret-token") || !strings.Contains(out, "Total: 8") || !strings.Contains(out, "not installs") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestFetchErrorsArePlain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer srv.Close()
	_, err := fetch(srv.URL, "o/r", "")
	if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("want a 404 message mentioning GITHUB_TOKEN, got %v", err)
	}
}

func TestEmpty(t *testing.T) {
	var buf bytes.Buffer
	printReport(&buf, nil)
	if !strings.Contains(buf.String(), "No published releases") {
		t.Fatal(buf.String())
	}
}
