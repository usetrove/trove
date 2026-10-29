package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	path := filepath.Join(dataDir, FileName)
	b, err := os.ReadFile(path) //nolint:gosec // path is inside the user's own data directory
	switch {
	case err == nil:
	case os.IsNotExist(err):
		b = []byte(DefaultTOML)
	default:
		return fmt.Errorf("cannot read %s: %v. Check its permissions.", path, err)
	}
	out := setBudgetLines(string(b), period, capUSD)
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
	nl := "\n"
	if strings.Contains(src, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	capLine := "cap_usd = " + strconv.FormatFloat(capUSD, 'f', -1, 64)
	if !strings.Contains(capLine, ".") {
		capLine += ".0"
	}
	periodLine := fmt.Sprintf("period = %q", period)

	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "[budget]" {
			start = i
			break
		}
	}
	if start < 0 {
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "", "[budget]", periodLine, capLine, "")
		return strings.Join(lines, nl)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, "[") {
			end = i
			break
		}
	}
	setKey := func(key, line string) {
		for i := start + 1; i < end; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, key) && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, key)), "=") {
				lines[i] = line
				return
			}
		}
		lines = append(lines[:start+1], append([]string{line}, lines[start+1:]...)...)
		end++
	}
	setKey("period", periodLine)
	setKey("cap_usd", capLine)
	return strings.Join(lines, nl)
}
