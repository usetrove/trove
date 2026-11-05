package fixer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSetJSONEnvString(t *testing.T) {
	tests := []struct {
		name, src, want string
		wantErr         string
	}{
		{"new file", "", "{\n  \"env\": {\n    \"A\": \"1\"\n  }\n}\n", ""},
		{"no env key", "{\n  \"model\": \"x\"\n}\n", "{\n  \"model\": \"x\",\n  \"env\": {\n    \"A\": \"1\"\n  }\n}\n", ""},
		{"empty env", "{\n  \"env\": {}\n}\n", "{\n  \"env\": {\n    \"A\": \"1\"\n  }\n}\n", ""},
		{"other env members", "{\n  \"env\": {\n    \"B\": \"2\"\n  }\n}\n", "{\n  \"env\": {\n    \"B\": \"2\",\n    \"A\": \"1\"\n  }\n}\n", ""},
		{"replace only the value", "{\n  \"env\": {\n    \"A\": \"old\", // note\n    \"B\": \"2\"\n  }\n}\n", "", "not valid JSON"},
		{"replace", "{\n  \"env\": {\n    \"A\": \"old\",\n    \"B\": \"2\"\n  }\n}\n", "{\n  \"env\": {\n    \"A\": \"1\",\n    \"B\": \"2\"\n  }\n}\n", ""},
		{"same value is unchanged", "{\n  \"env\": {\n    \"A\": \"1\"\n  }\n}\n", "{\n  \"env\": {\n    \"A\": \"1\"\n  }\n}\n", ""},
		{"single line file", `{"model": "x"}`, `{"model": "x", "env": {"A": "1"}}`, ""},
		{"env not an object", `{"env": "x"}`, "", "not an object"},
		{"value not a string", `{"env": {"A": 1}}`, "", "not a string"},
		{"crlf survives", "{\r\n  \"model\": \"x\"\r\n}\r\n", "{\r\n  \"model\": \"x\",\r\n  \"env\": {\r\n    \"A\": \"1\"\r\n  }\r\n}\r\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SetJSONEnvString([]byte(tc.src), "A", "1")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
			var doc map[string]any
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Errorf("result is not valid JSON: %v", err)
			}
			if v, ok := JSONEnvValue(got, "A"); !ok || v != "1" {
				t.Errorf("env.A = %q, %v", v, ok)
			}
		})
	}
}

func TestSetJSONEnvStringIdempotent(t *testing.T) {
	once, err := SetJSONEnvString([]byte("{\n  \"model\": \"x\"\n}\n"), "A", "1")
	if err != nil {
		t.Fatal(err)
	}
	twice, err := SetJSONEnvString(once, "A", "1")
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Errorf("second application changed the file:\n%s", twice)
	}
}
