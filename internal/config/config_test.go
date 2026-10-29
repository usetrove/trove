package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDefaultTOMLMatchesDefault(t *testing.T) {
	var got Config
	if err := toml.Unmarshal([]byte(DefaultTOML), &got); err != nil {
		t.Fatal(err)
	}
	if got.Optimizers == nil {
		got.Optimizers = map[string]string{}
	}
	if want := Default(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultTOML drifted from Default():\n got %+v\nwant %+v", got, want)
	}
	if err := Default().Validate(); err != nil {
		t.Errorf("defaults are invalid: %v", err)
	}
}

func TestDefaultTOMLGolden(t *testing.T) {
	golden := filepath.Join("testdata", "default.toml")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(golden, []byte(DefaultTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden file; run with UPDATE_GOLDEN=1: %v", err)
	}
	if strings.ReplaceAll(string(b), "\r\n", "\n") != DefaultTOML {
		t.Error("DefaultTOML changed; review it, then run with UPDATE_GOLDEN=1")
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		file    string // empty = no file
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr []string // substrings
	}{
		{name: "no file gives defaults", check: func(t *testing.T, c Config) {
			if !reflect.DeepEqual(c, Default()) {
				t.Errorf("got %+v", c)
			}
		}},
		{name: "file overrides", file: "retention_days = 30\n[gateway]\nport = 9000\n[optimizers]\ntrim = \"shadow\"\n",
			check: func(t *testing.T, c Config) {
				if c.RetentionDays != 30 || c.Gateway.Port != 9000 || c.OptimizerState("trim") != StateShadow {
					t.Errorf("got %+v", c)
				}
				if c.Upstreams.Anthropic != "https://api.anthropic.com" {
					t.Error("unset values must keep defaults")
				}
			}},
		{name: "unlisted optimizer is off", check: func(t *testing.T, c Config) {
			if c.OptimizerState("anything") != StateOff {
				t.Error("want off")
			}
		}},
		{name: "env beats file", file: "[gateway]\nport = 9000\n", env: map[string]string{"TROVE_GATEWAY_PORT": "9100", "TROVE_BUDGET_CAP_USD": "12.5"},
			check: func(t *testing.T, c Config) {
				if c.Gateway.Port != 9100 || c.Budget.CapUSD != 12.5 {
					t.Errorf("got %+v", c)
				}
			}},
		{name: "env upstream", env: map[string]string{"TROVE_UPSTREAMS_OPENAI": "http://127.0.0.1:4000"},
			check: func(t *testing.T, c Config) {
				if c.Upstreams.OpenAI != "http://127.0.0.1:4000" {
					t.Errorf("got %+v", c.Upstreams)
				}
			}},
		{name: "bad port", file: "[gateway]\nport = 70000\n", wantErr: []string{"gateway.port is 70000", "1 to 65535"}},
		{name: "bad env int", env: map[string]string{"TROVE_GATEWAY_PORT": "abc"}, wantErr: []string{"TROVE_GATEWAY_PORT", "whole number"}},
		{name: "bad env float", env: map[string]string{"TROVE_BUDGET_CAP_USD": "lots"}, wantErr: []string{"TROVE_BUDGET_CAP_USD"}},
		{name: "bad optimizer state", file: "[optimizers]\ntrim = \"maybe\"\n", wantErr: []string{"optimizers.trim", "\"off\", \"shadow\" or \"on\""}},
		{name: "claude code cannot be custom", file: "[optimize]\ncustom_agents = [\"claude_code\"]\n", wantErr: []string{"claude_code", "manage their own prompts"}},
		{name: "bad optimize budget", file: "[optimize]\nbudget_ms = 0\n", wantErr: []string{"optimize.budget_ms is 0"}},
		{name: "bad optimizer name", file: "[optimizers]\n\"Bad Name\" = \"on\"\n", wantErr: []string{"not a valid optimizer name"}},
		{name: "http upstream rejected", file: "[upstreams]\nanthropic = \"http://example.com\"\n", wantErr: []string{"upstreams.anthropic", "https://"}},
		{name: "upstream with path rejected", file: "[upstreams]\nopenai = \"https://api.openai.com/v1\"\n", wantErr: []string{"origin only"}},
		{name: "bad budget period", file: "[budget]\nperiod = \"yearly\"\n", wantErr: []string{"budget.period"}},
		{name: "negative cap", file: "[budget]\ncap_usd = -1\n", wantErr: []string{"budget.cap_usd"}},
		{name: "bad retention", file: "retention_days = 0\n", wantErr: []string{"retention_days is 0"}},
		{name: "multiple errors reported together", file: "retention_days = 0\n[gateway]\nport = 0\n", wantErr: []string{"retention_days", "gateway.port"}},
		{name: "unknown key", file: "[gateway]\nprot = 1\n", wantErr: []string{"unknown setting", "prot"}},
		{name: "syntax error", file: "[gateway\n", wantErr: []string{"line 1", "delete the file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.file != "" {
				dir = writeCfg(t, tt.file)
			}
			c, err := Load(dir, env(tt.env))
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatal("expected an error")
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, c)
		})
	}
}

func TestResolveDataDir(t *testing.T) {
	abs := t.TempDir()
	got, err := ResolveDataDir(abs, env(nil))
	if err != nil || got != abs {
		t.Errorf("flag: got %q, %v", got, err)
	}
	other := t.TempDir()
	got, _ = ResolveDataDir("", env(map[string]string{"TROVE_DATA_DIR": other}))
	if got != other {
		t.Errorf("env: got %q", got)
	}
	got, err = ResolveDataDir("", env(nil))
	if err != nil || filepath.Base(got) != ".trove" {
		t.Errorf("default: got %q, %v", got, err)
	}
}
