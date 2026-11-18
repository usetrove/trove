package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetVerbosity(t *testing.T) {
	cases := []struct {
		name, start          string
		wantValue            string
		wantHand, wantLearnd bool
	}{
		{"no file", "", VerbosityConcise, false, false},
		{"default file", DefaultTOML, VerbosityConcise, false, false},
		{"hand-set terse", "[optimize]\nverbosity = \"terse\"\n", VerbosityTerse, true, false},
		{"no optimize section", "[budget]\nperiod = \"daily\"\n", VerbosityConcise, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.start != "" {
				if err := os.WriteFile(filepath.Join(dir, FileName), []byte(c.start), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s, err := ReadVerbositySetting(dir)
			if err != nil {
				t.Fatal(err)
			}
			if s.Value != c.wantValue || s.HandSet != c.wantHand || s.Learned != c.wantLearnd {
				t.Errorf("before: %+v", s)
			}
			if err := SetVerbosity(dir, VerbosityTerse); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(dir, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Optimize.Verbosity != VerbosityTerse {
				t.Errorf("verbosity after set = %q", cfg.Optimize.Verbosity)
			}
			s, _ = ReadVerbositySetting(dir)
			if !s.Learned || s.HandSet || s.Value != VerbosityTerse {
				t.Errorf("after: %+v; a learned value must not count as hand-set", s)
			}
			b, _ := os.ReadFile(filepath.Join(dir, FileName))
			if strings.Count(string(b), "verbosity =") != 1 {
				t.Errorf("one verbosity line expected:\n%s", b)
			}
			if c.start == DefaultTOML && !strings.Contains(string(b), "budget_ms = 25") {
				t.Error("other settings must be kept")
			}
		})
	}
	if err := SetVerbosity(t.TempDir(), "loud"); err == nil {
		t.Error("an unknown level must be refused")
	}
}
