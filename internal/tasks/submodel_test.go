package tasks

import "testing"

func TestSetModel(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want, reason string
	}{
		"adds the line":      {"---\nname: a\ndescription: d\n---\n\nbody\n", "---\nname: a\ndescription: d\nmodel: haiku\n---\n\nbody\n", ""},
		"keeps CRLF":         {"---\r\nname: a\r\n---\r\nbody", "---\r\nname: a\r\nmodel: haiku\r\n---\r\nbody", ""},
		"user chose":         {"---\nname: a\nmodel: opus\n---\n", "", ""},
		"user chose inherit": {"---\nname: a\nmodel: inherit\n---\n", "", ""},
		"no frontmatter":     {"just text", "", "the file does not start"},
		"unclosed":           {"---\nname: a\n", "", "no closing"},
		"trove's own":        {"---\nname: trove-scout\n---\n" + Managed + "\n", "", "Trove wrote"},
		"body mention only":  {"---\nname: a\n---\nmodel: in body\n", "---\nname: a\nmodel: haiku\n---\nmodel: in body\n", ""},
	} {
		got, why := SetModel([]byte(tc.in), "haiku")
		if string(got) != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
		if (tc.reason == "") != (why == "") || (tc.reason != "" && !contains(why, tc.reason)) {
			t.Errorf("%s: reason %q, want one containing %q", name, why, tc.reason)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
