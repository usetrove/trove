package external

import (
	"strings"
	"testing"
)

const goodManifest = `
name = "demo"
version = "1.2.3"
license = "MIT"
source_url = "https://example.com/demo"
verified_on = "2026-11-04"
acts_as = "shell_wrapper"
category = "shell_compressor"
points = ["tool_output"]
[install]
argv = ["echo", "hi"]
[claimed]
text = "claims 60-90%"
source_url = "https://example.com/demo"
`

func TestParseValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{"good", func(s string) string { return s }, ""},
		{"unpinned", func(s string) string { return strings.Replace(s, `"1.2.3"`, `"latest"`, 1) }, "pinned"},
		{"range", func(s string) string { return strings.Replace(s, `"1.2.3"`, `"^1.2"`, 1) }, "pinned"},
		{"no license", func(s string) string { return strings.Replace(s, `license = "MIT"`, `license = ""`, 1) }, "license"},
		{"http", func(s string) string {
			return strings.Replace(s, `source_url = "https://example.com/demo"`, `source_url = "http://x"`, 1)
		}, "https"},
		{"bad point", func(s string) string { return strings.Replace(s, `"tool_output"`, `"nope"`, 1) }, "engine point"},
		{"bad acts", func(s string) string { return strings.Replace(s, `"shell_wrapper"`, `"magic"`, 1) }, "acts_as"},
		{"unknown field", func(s string) string { return s + "\nsurprise = 1\n" }, "could not read"},
		{"empty argv program", func(s string) string { return strings.Replace(s, `["echo", "hi"]`, `[""]`, 1) }, "install.argv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.mutate(goodManifest)))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestBuiltinManifestsValid(t *testing.T) {
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.OptimizerName() != "ext_"+m.Name {
			t.Errorf("bad optimizer name for %s", m.Name)
		}
	}
}

func TestClassifyLicense(t *testing.T) {
	cases := map[string]LicenseClass{
		"MIT": LicensePermissive, "Apache-2.0": LicensePermissive, "Trove-project": LicensePermissive,
		"GPL-3.0": LicenseCopyleft, "AGPL-3.0": LicenseCopyleft,
		"Elastic-2.0": LicenseRestricted, "PolyForm-Noncommercial-1.0.0": LicenseRestricted,
		"BUSL-1.1": LicenseRestricted, "SSPL-1.0": LicenseRestricted,
		"MIT OR Apache-2.0":   LicensePermissive,
		"MIT AND Elastic-2.0": LicenseRestricted,
		"Weird-Custom":        LicenseUnknown, "": LicenseUnknown,
	}
	for in, want := range cases {
		if got := ClassifyLicense(in); got != want {
			t.Errorf("ClassifyLicense(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestNoticeWarnsForRestricted(t *testing.T) {
	for _, lic := range []string{"Elastic-2.0", "PolyForm-Noncommercial-1.0.0", "Mystery"} {
		n := NoticeFor(Manifest{Name: "x", License: lic, SourceURL: "https://x"})
		if n.Warning == "" || !strings.HasPrefix(n.Warning, "WARNING") || !n.NeedsTypedConfirm {
			t.Errorf("%s: want a WARNING and typed confirm, got %+v", lic, n)
		}
	}
	if n := NoticeFor(Manifest{License: "MIT"}); n.Warning != "" || n.NeedsTypedConfirm {
		t.Errorf("MIT should not warn: %+v", n)
	}
}

func TestCheckEnable(t *testing.T) {
	rtk := Manifest{Name: "rtk", Category: CategoryShellCompressor}
	other := Manifest{Name: "squeeze", Category: CategoryShellCompressor}
	nav := Manifest{Name: "nav", Category: CategoryCodeNav}
	explicit := Manifest{Name: "a", Category: "misc", ConflictsWith: []string{"b"}}
	b := Manifest{Name: "b", Category: "misc"}

	cases := []struct {
		name     string
		want     Manifest
		enabled  []Manifest
		builtins []string
		n        int
		other    string
	}{
		{"two shell compressors", other, []Manifest{rtk}, nil, 1, "rtk"},
		{"self ignored", rtk, []Manifest{rtk}, nil, 0, ""},
		{"different category", nav, []Manifest{rtk}, nil, 0, ""},
		{"explicit one way", explicit, []Manifest{b}, nil, 1, "b"},
		{"explicit other way", b, []Manifest{explicit}, nil, 1, "a"},
		{"builtin overlap", rtk, nil, []string{"output_trimmer"}, 1, "output_trimmer"},
		{"builtin unrelated", rtk, nil, []string{"cache_breakpoints"}, 0, ""},
		{"code nav ignores builtins", nav, nil, []string{"output_trimmer"}, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckEnable(c.want, c.enabled, c.builtins)
			if len(got) != c.n {
				t.Fatalf("want %d conflicts, got %v", c.n, got)
			}
			if c.n > 0 && got[0].Other != c.other {
				t.Errorf("other = %q, want %q", got[0].Other, c.other)
			}
		})
	}
}

func TestValidateStates(t *testing.T) {
	a := Manifest{Name: "a", Category: CategoryShellCompressor}
	b := Manifest{Name: "b", Category: CategoryShellCompressor}
	n := Manifest{Name: "n", Category: CategoryCodeNav, SupportsShadow: true}
	ms := []Manifest{a, b, n}
	cases := []struct {
		name     string
		states   map[string]string
		builtins []string
		want     int
		contains string
	}{
		{"empty", nil, nil, 0, ""},
		{"unknown tool", map[string]string{"ext_zzz": "on"}, nil, 1, "Trove knows"},
		{"shadow refused", map[string]string{"ext_a": "shadow"}, nil, 1, "cannot run in shadow"},
		{"shadow allowed", map[string]string{"ext_n": "shadow"}, nil, 0, ""},
		{"two compressors on", map[string]string{"ext_a": "on", "ext_b": "on"}, nil, 1, "disable"},
		{"one on one off", map[string]string{"ext_a": "on", "ext_b": "off"}, nil, 0, ""},
		{"builtin overlap", map[string]string{"ext_a": "on"}, []string{"noise_normalizer"}, 1, "built-in"},
		{"non-external ignored", map[string]string{"output_trimmer": "shadow"}, nil, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := ValidateStates(c.states, ms, c.builtins)
			if len(errs) != c.want {
				t.Fatalf("want %d errors, got %v", c.want, errs)
			}
			if c.want > 0 && !strings.Contains(errs[0].Error(), c.contains) {
				t.Errorf("error %q lacks %q", errs[0], c.contains)
			}
		})
	}
}
