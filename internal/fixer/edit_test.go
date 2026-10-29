package fixer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeleteLinesKeepsEverythingElse(t *testing.T) {
	src := "# T\r\nkeep\r\n## Drop\r\ngone\r\n## Keep\r\nlast no newline"
	got := string(DeleteLines([]byte(src), []LineRange{{2, 4}}))
	want := "# T\r\nkeep\r\n## Keep\r\nlast no newline"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if string(DeleteLines([]byte(src), nil)) != src {
		t.Fatal("no ranges must be a no-op")
	}
}

func TestSetJSONString(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"replace keeps formatting", "{\n  \"a\": 1,\n  \"model\":   \"claude-opus-5-5\",\n  \"z\": [1,2]\n}\n",
			"{\n  \"a\": 1,\n  \"model\":   \"cheap\",\n  \"z\": [1,2]\n}\n"},
		{"insert multiline", "{\n  \"a\": 1\n}\n", "{\n  \"a\": 1,\n  \"model\": \"cheap\"\n}\n"},
		{"insert crlf", "{\r\n  \"a\": 1\r\n}\r\n", "{\r\n  \"a\": 1,\r\n  \"model\": \"cheap\"\r\n}\r\n"},
		{"insert single line", `{"a":1}`, `{"a":1, "model": "cheap"}`},
		{"empty object", "{}", "{\n  \"model\": \"cheap\"\n}"},
		{"new file", "", "{\n  \"model\": \"cheap\"\n}\n"},
		{"braces in strings", `{"s": "} {", "model": "x"}`, `{"s": "} {", "model": "cheap"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SetJSONString([]byte(c.in), "model", "cheap")
			if err != nil || string(got) != c.want {
				t.Fatalf("got %q err %v\nwant %q", got, err, c.want)
			}
		})
	}
	if _, err := SetJSONString([]byte(`{"model": 3}`), "model", "x"); err == nil {
		t.Fatal("a non-string model must be refused")
	}
	if _, err := SetJSONString([]byte(`{"a": 1,}`), "model", "x"); err == nil {
		t.Fatal("invalid JSON must be refused")
	}
}

func TestAddToJSONStringArray(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"new key", "{\n  \"a\": 1\n}\n", "{\n  \"a\": 1,\n  \"d\": [\"x\"]\n}\n"},
		{"empty array", `{"d": []}`, `{"d": ["x"]}`},
		{"inline array", `{"d": ["a", "b"]}`, `{"d": ["a", "b", "x"]}`},
		{"multiline array", "{\n  \"d\": [\n    \"a\",\n    \"b\"\n  ]\n}\n", "{\n  \"d\": [\n    \"a\",\n    \"b\",\n    \"x\"\n  ]\n}\n"},
		{"already there", `{"d": ["x"]}`, `{"d": ["x"]}`},
		{"new file", "", "{\n  \"d\": [\"x\"]\n}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AddToJSONStringArray([]byte(c.in), "d", "x")
			if err != nil || string(got) != c.want {
				t.Fatalf("got %q err %v\nwant %q", got, err, c.want)
			}
			if !json.Valid(got) {
				t.Fatal("result is not valid JSON")
			}
		})
	}
	if _, err := AddToJSONStringArray([]byte(`{"d": "no"}`), "d", "x"); err == nil {
		t.Fatal("a non-array must be refused")
	}
}

func TestDisableMCPServerTOML(t *testing.T) {
	in := "model = \"gpt\"\n\n# keep this comment\n[mcp_servers.alpha]\ncommand = \"a\"\n\n[mcp_servers.beta]\nurl = \"http://x\"\nenabled = true # on\n"
	got, err := DisableMCPServerTOML([]byte(in), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	want := "model = \"gpt\"\n\n# keep this comment\n[mcp_servers.alpha]\nenabled = false\ncommand = \"a\"\n\n[mcp_servers.beta]\nurl = \"http://x\"\nenabled = true # on\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
	got, err = DisableMCPServerTOML([]byte(in), "beta")
	if err != nil || !strings.HasSuffix(string(got), "enabled = false # on\n") {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := DisableMCPServerTOML([]byte(in), "missing"); err == nil {
		t.Fatal("unknown server must be refused")
	}
	if _, err := DisableMCPServerTOML([]byte("mcp_servers = { a = { command = \"x\" } }\n"), "a"); err == nil {
		t.Fatal("inline tables must be refused, not guessed at")
	}
}

func TestSetTOMLTopString(t *testing.T) {
	got, err := SetTOMLTopString([]byte("model = \"big\" # why\n[x]\nmodel = \"other\"\n"), "model", "small")
	if err != nil || string(got) != "model = \"small\" # why\n[x]\nmodel = \"other\"\n" {
		t.Fatalf("got %q %v", got, err)
	}
	got, err = SetTOMLTopString([]byte("[x]\na = 1\n"), "model", "small")
	if err != nil || string(got) != "model = \"small\"\n[x]\na = 1\n" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestUnifiedDiff(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	upd := "a\nb\nc\nd\ne\nf\ng\nh\nj\n"
	want := "--- f\n+++ f\n@@ -6,5 +6,4 @@\n f\n g\n h\n-i\n j\n"
	if got := UnifiedDiff("f", []byte(old), []byte(upd)); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if UnifiedDiff("f", []byte(old), []byte(old)) != "" {
		t.Fatal("identical files have an empty diff")
	}
	if d := UnifiedDiff("f", []byte("x"), []byte("x\ny\n")); !strings.Contains(d, "\\ No newline at end of file") {
		t.Fatalf("missing no-newline marker: %s", d)
	}
}
