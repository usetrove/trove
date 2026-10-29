package analyze

import (
	"regexp"
	"strings"
)

var (
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	progressRe = regexp.MustCompile(`\d{1,3}(?:\.\d+)?%.*[#=█▓░>]{4,}|[#=█▓░>]{8,}.*\d{1,3}(?:\.\d+)?%|\d+(?:\.\d+)?\s?[kKMG]i?B/s`)
	spinnerRe  = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]|^\s*[|/\\-]\s*\S{0,40}$`)
)

const (
	minIdenticalRun   = 5  // identical consecutive lines before the repeats count as noise
	minPassingLines   = 20 // passing-test lines before the list counts as noise
	minNoiseBytes     = 256
	lineClassProgress = 1
	lineClassPassing  = 2
	lineClassRepeat   = 3
)

// Noise is the byte count of tool output that carries no information for the
// model, by kind. Counts only; the text is not kept.
type Noise struct {
	Total int
	ANSI  int
	Progr int // progress bars, spinner frames and carriage-return redraws
	Pass  int // long lists of passing tests
	Repea int // long runs of identical lines
}

// measureNoise scans one tool result. It counts what a lossless cleaner could
// drop while leaving the information: the last frame of a progress bar, one
// line of a repeated run, one line of a passing-test list, and escape codes.
func measureNoise(text string) Noise {
	var n Noise
	if len(text) < minNoiseBytes {
		return n
	}
	lines := strings.Split(text, "\n")
	class := make([]uint8, len(lines))

	// Carriage-return redraws: every frame but the last is noise.
	for i, l := range lines {
		if strings.Contains(l, "\r") {
			parts := strings.Split(strings.TrimRight(l, "\r"), "\r")
			for _, p := range parts[:len(parts)-1] {
				n.Progr += len(p) + 1
			}
			lines[i] = parts[len(parts)-1]
		}
	}
	// Progress and spinner lines: all but the last of each consecutive group.
	for i, l := range lines {
		if len(l) <= 200 && (isProgress(l) || isSpinner(l)) {
			class[i] = lineClassProgress
		}
	}
	// Counted frames stay marked so later passes skip them; the final frame of
	// a group is information and is released.
	for i := 0; i < len(lines); i++ {
		if class[i] != lineClassProgress {
			continue
		}
		if i+1 < len(lines) && class[i+1] == lineClassProgress {
			n.Progr += len(lines[i]) + 1
		} else {
			class[i] = 0
		}
	}

	// Passing-test lists.
	passes, passBytes, firstPass := 0, 0, -1
	for i, l := range lines {
		if class[i] == 0 && isPassing(l) {
			class[i] = lineClassPassing
			passes++
			passBytes += len(l) + 1
			if firstPass < 0 {
				firstPass = i
			}
		}
	}
	if passes >= minPassingLines {
		n.Pass = passBytes - (len(lines[firstPass]) + 1)
	} else {
		for i := range class {
			if class[i] == lineClassPassing {
				class[i] = 0
			}
		}
	}

	// Runs of identical lines.
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && class[i] == 0 && class[j] == 0 && lines[j] == lines[i] {
			j++
		}
		if run := j - i; run >= minIdenticalRun {
			n.Repea += (run - 1) * (len(lines[i]) + 1)
			for k := i + 1; k < j; k++ {
				class[k] = lineClassRepeat
			}
		}
		i = j
	}

	// Escape codes on lines that were not already counted.
	for i, l := range lines {
		if class[i] == 0 && strings.IndexByte(l, 0x1b) >= 0 {
			for _, loc := range ansiRe.FindAllStringIndex(l, -1) {
				n.ANSI += loc[1] - loc[0]
			}
		}
	}
	n.Total = n.ANSI + n.Progr + n.Pass + n.Repea
	if n.Total < minNoiseBytes {
		return Noise{}
	}
	return n
}

// The line classifiers check cheap byte conditions first so the regular
// expressions only run on lines that could match; they dominate the cost
// otherwise.

func isProgress(l string) bool {
	return (strings.Contains(l, "%") || strings.Contains(l, "B/s")) && progressRe.MatchString(l)
}

func isSpinner(l string) bool {
	if strings.ContainsAny(l, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		return true
	}
	if len(l) > 50 {
		return false
	}
	t := strings.TrimLeft(l, " \t")
	return t != "" && strings.IndexByte(`|/\-`, t[0]) >= 0 && spinnerRe.MatchString(l)
}

func isPassing(l string) bool {
	t := strings.TrimLeft(l, " \t")
	switch {
	case strings.HasPrefix(t, "--- PASS:"), strings.HasPrefix(t, "✓"), strings.HasPrefix(t, "✔"):
		return true
	case strings.HasPrefix(t, "PASS") && (len(t) == 4 || !isWordByte(t[4])):
		return true
	case strings.HasPrefix(t, "ok") && len(t) > 2 && (t[2] == ' ' || t[2] == '\t') && strings.TrimSpace(t[2:]) != "":
		return true
	case t != "" && strings.Contains(t, ".") && strings.Trim(t, ". \t\r") == "":
		return true
	}
	// pytest style: "tests/test_a.py::test_x PASSED"
	if i := strings.LastIndex(t, " PASSED"); i > 0 {
		rest := t[i+len(" PASSED"):]
		return rest == "" || !isWordByte(rest[0])
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
