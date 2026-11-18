package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SetBudget writes the budget period and cap to <dataDir>/config.toml. It edits
// only those two lines, so comments and every other setting stay as they are. A
// missing file is created from DefaultTOML first. period must be "daily",
// "weekly" or "monthly"; capUSD is 0 (no cap) or positive.
func SetBudget(dataDir, period string, capUSD float64) error {
	switch period {
	case "daily", "weekly", "monthly":
	default:
		return fmt.Errorf("budget period is %q; use \"daily\", \"weekly\" or \"monthly\".", period)
	}
	if capUSD < 0 {
		return fmt.Errorf("budget amount is %g; use a positive amount, or 0 for no cap.", capUSD)
	}
	return editConfigFile(dataDir, func(src string) string { return setBudgetLines(src, period, capUSD) })
}

// readConfigText returns config.toml's text, or DefaultTOML when there is none.
func readConfigText(dataDir string) (string, error) {
	path := filepath.Join(dataDir, FileName)
	b, err := os.ReadFile(path) //nolint:gosec // path is inside the user's own data directory
	switch {
	case err == nil:
		return string(b), nil
	case os.IsNotExist(err):
		return DefaultTOML, nil
	default:
		return "", fmt.Errorf("cannot read %s: %v. Check its permissions.", path, err)
	}
}

// editConfigFile rewrites config.toml through edit, atomically and with mode 0600. A
// missing file is created from DefaultTOML first.
func editConfigFile(dataDir string, edit func(string) string) error {
	path := filepath.Join(dataDir, FileName)
	src, err := readConfigText(dataDir)
	if err != nil {
		return err
	}
	out := edit(src)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %v. Check its permissions.", dataDir, err)
	}
	tmp, err := os.CreateTemp(dataDir, FileName+".*")
	if err != nil {
		return fmt.Errorf("cannot write %s: %v. Check its permissions.", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cannot write %s: %v.", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %v.", path, err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("cannot set permissions on %s: %v.", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cannot replace %s: %v.", path, err)
	}
	return nil
}

// setBudgetLines replaces or adds the period and cap_usd lines inside [budget].
func setBudgetLines(src, period string, capUSD float64) string {
	capLine := "cap_usd = " + strconv.FormatFloat(capUSD, 'f', -1, 64)
	if !strings.Contains(capLine, ".") {
		capLine += ".0"
	}
	return setSectionLines(src, "budget", [][2]string{{"period", fmt.Sprintf("period = %q", period)}, {"cap_usd", capLine}})
}

// sectionBounds returns the line index of [section]'s header and the index just past
// its last line, or start -1 when it is missing.
func sectionBounds(lines []string, section string) (start, end int) {
	start = -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "["+section+"]" {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, "[") {
			end = i
			break
		}
	}
	return start, end
}

// keyLine returns the index of key's line in lines[start+1:end], or -1.
func keyLine(lines []string, start, end int, key string) int {
	for i := start + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, key) && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, key)), "=") {
			return i
		}
	}
	return -1
}

// setSectionLines replaces or adds each {key, full line} inside [section], keeping every
// other line, comment and the file's line endings as they are.
func setSectionLines(src, section string, kv [][2]string) string {
	nl := "\n"
	if strings.Contains(src, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	start, end := sectionBounds(lines, section)
	if start < 0 {
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "", "["+section+"]")
		for _, p := range kv {
			lines = append(lines, p[1])
		}
		return strings.Join(append(lines, ""), nl)
	}
	for _, p := range kv {
		if i := keyLine(lines, start, end, p[0]); i >= 0 {
			lines[i] = p[1]
			continue
		}
		lines = append(lines[:start+1], append([]string{p[1]}, lines[start+1:]...)...)
		end++
	}
	return strings.Join(lines, nl)
}

// GatewayBehaviors lists the behaviors of the gateway itself that carry an optimizer
// state ("off", "shadow" or "on") like the pipeline's optimizers do, but act on which
// requests reach the provider and not on the bytes of one. Only keep_warm has an active
// mode; for the others "on" runs as shadow (see GatewayActive).
var GatewayBehaviors = []string{OptimizerInflightDedup, OptimizerSpendCap, OptimizerKeepWarm}

// GatewayActive reports whether a gateway behavior has a built active mode, so "on" means on.
func GatewayActive(name string) bool { return name == OptimizerKeepWarm }

// IsGatewayBehavior reports whether name is one of GatewayBehaviors.
func IsGatewayBehavior(name string) bool {
	for _, g := range GatewayBehaviors {
		if g == name {
			return true
		}
	}
	return false
}

// Window returns the budget period that contains now, in now's location: from is
// its first instant and to the first instant after it. Weeks start on Monday. It
// is the definition the report and the status line use, so a cap measured here
// agrees with the pacing a person sees.
func (b Budget) Window(now time.Time) (from, to time.Time) {
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch b.Period {
	case "weekly":
		from = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		return from, from.AddDate(0, 0, 7)
	case "daily":
		return day, day.AddDate(0, 0, 1)
	}
	from = time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	return from, from.AddDate(0, 1, 0)
}
