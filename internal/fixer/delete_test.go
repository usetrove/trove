package fixer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeleteJSONEnvKey(t *testing.T) {
	cases := []struct {
		name, in, key, want string
	}{
		{"middle member",
			"{\n  \"env\": {\n    \"A\": \"1\",\n    \"DISABLE_PROMPT_CACHING\": \"1\",\n    \"B\": \"2\"\n  },\n  \"model\": \"x\"\n}\n",
			"DISABLE_PROMPT_CACHING",
			"{\n  \"env\": {\n    \"A\": \"1\",\n    \"B\": \"2\"\n  },\n  \"model\": \"x\"\n}\n"},
		{"first member",
			"{\n  \"env\": {\n    \"DISABLE_PROMPT_CACHING\": \"1\",\n    \"B\": \"2\"\n  }\n}\n",
			"DISABLE_PROMPT_CACHING",
			"{\n  \"env\": {\n    \"B\": \"2\"\n  }\n}\n"},
		{"last member",
			"{\n  \"env\": {\n    \"A\": \"1\",\n    \"DISABLE_PROMPT_CACHING\": \"1\"\n  }\n}\n",
			"DISABLE_PROMPT_CACHING",
			"{\n  \"env\": {\n    \"A\": \"1\"\n  }\n}\n"},
		{"only member leaves an empty object",
			"{\n  \"env\": {\n    \"DISABLE_PROMPT_CACHING\": \"1\"\n  },\n  \"x\": 1\n}\n",
			"DISABLE_PROMPT_CACHING",
			"{\n  \"env\": {},\n  \"x\": 1\n}\n"},
		{"single line",
			`{"env": {"A": "1", "ENABLE_TOOL_SEARCH": "false"}}`,
			"ENABLE_TOOL_SEARCH",
			`{"env": {"A": "1"}}`},
		{"windows line endings",
			"{\r\n  \"env\": {\r\n    \"A\": \"1\",\r\n    \"ENABLE_TOOL_SEARCH\": \"false\"\r\n  }\r\n}\r\n",
			"ENABLE_TOOL_SEARCH",
			"{\r\n  \"env\": {\r\n    \"A\": \"1\"\r\n  }\r\n}\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DeleteJSONEnvKey([]byte(c.in), c.key)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, c.want)
			}
			if !json.Valid(got) {
				t.Error("result is not valid JSON")
			}
		})
	}
}

func TestDeleteJSONKey(t *testing.T) {
	cases := []struct{ name, in, key, want string }{
		{"middle", "{\n  \"a\": 1,\n  \"promptCacheTtl\": \"1h\",\n  \"b\": 2\n}\n", "promptCacheTtl", "{\n  \"a\": 1,\n  \"b\": 2\n}\n"},
		{"last", "{\n  \"a\": 1,\n  \"promptCacheTtl\": \"1h\"\n}\n", "promptCacheTtl", "{\n  \"a\": 1\n}\n"},
		{"only", "{\n  \"promptCacheTtl\": \"1h\"\n}\n", "promptCacheTtl", "{}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DeleteJSONKey([]byte(c.in), c.key)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want || !json.Valid(got) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDeleteRefusesWhatItCannotDoSafely(t *testing.T) {
	for name, tc := range map[string]struct {
		fn      func([]byte, string) ([]byte, error)
		in, key string
		want    string
	}{
		"key missing":        {DeleteJSONKey, `{"a":1}`, "b", "is not in this file"},
		"env missing":        {DeleteJSONEnvKey, `{"a":1}`, "A", `no "env" object`},
		"env var missing":    {DeleteJSONEnvKey, `{"env":{"B":"1"}}`, "A", "is not in this file"},
		"env is not object":  {DeleteJSONEnvKey, `{"env":"x"}`, "A", "not an object"},
		"not JSON (comment)": {DeleteJSONKey, "{\n// c\n\"a\":1}", "a", "not valid JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.fn([]byte(tc.in), tc.key); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

// Deleting never touches any other member's text.
func TestDeleteLeavesOtherMembersByteForByte(t *testing.T) {
	in := "{\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]},\n  \"env\": {\n    \"KEEP\": \"a  b\",\n    \"DISABLE_PROMPT_CACHING\": \"1\"\n  },\n  \"statusLine\": {\"type\": \"command\"}\n}\n"
	got, err := DeleteJSONEnvKey([]byte(in), "DISABLE_PROMPT_CACHING")
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{`"permissions": {"allow": ["Bash(ls)"]}`, `"KEEP": "a  b"`, `"statusLine": {"type": "command"}`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("lost %s in:\n%s", keep, got)
		}
	}
}
