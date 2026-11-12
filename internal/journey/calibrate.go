package journey

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "embed"
)

// Calibration checks that the generator's tool output looks like real Claude Code
// session output, using only aggregates: the size of outputs per tool (median and
// 90th percentile), the share of outputs with color codes, and the share of lines
// that repeat an earlier line in the same output.
//
// The source is one developer's sessions on one machine (Windows, 210 Claude Code
// session files). It is not a sample of users. Its files are split into two halves
// by sorted index: the generator's parameters were fit to the even half, and the
// gate is checked on the odd half, which the fit never saw. A pass therefore says
// the generator reproduces the holdout half, not that it reproduces every user.

// calibrationSeed fixes the generated sample the gate compares, so the gate has
// one answer for everyone.
const calibrationSeed = 20261108

// calibrationSamples is how many generated outputs each tool class is compared on.
const calibrationSamples = 1500

// Aggregate is the content-free summary of one tool class.
type Aggregate struct {
	N        int     `json:"n"`
	MedianB  int     `json:"median_bytes"`
	P90B     int     `json:"p90_bytes"`
	ANSI     float64 `json:"color_code_share"`
	RepeatLn float64 `json:"repeated_line_share"`
}

// Targets is the committed calibration data. It holds aggregates only.
type Targets struct {
	Source  string               `json:"source"`
	Files   int                  `json:"log_files"`
	Results int                  `json:"tool_results"`
	Split   string               `json:"split"`
	Fit     map[string]Aggregate `json:"fit"`
	Holdout map[string]Aggregate `json:"holdout"`
}

//go:embed calibration_targets.json
var committedTargetsJSON []byte

// CommittedTargets returns the calibration data committed with this build.
func CommittedTargets() (Targets, error) {
	var t Targets
	if err := json.Unmarshal(committedTargetsJSON, &t); err != nil {
		return t, fmt.Errorf("the committed calibration targets are unreadable: %w", err)
	}
	return t, nil
}

// Tolerance says how far a generated aggregate may be from the target and still pass.
type Tolerance struct {
	// SizeRatio is the largest allowed ratio between a size and its target (1.25 = 25%).
	SizeRatio float64 `json:"size_ratio"`
	// ShareAbs is the largest allowed absolute difference for a share (0.05 = 5 points).
	ShareAbs float64 `json:"share_abs"`
}

// DefaultTolerance is the gate's tolerance. Changing it is a change to the gate.
var DefaultTolerance = Tolerance{SizeRatio: 1.25, ShareAbs: 0.05}

// ToolCheck is one tool class compared with the holdout target.
type ToolCheck struct {
	Tool   string    `json:"tool"`
	Target Aggregate `json:"target"`
	Got    Aggregate `json:"generated"`
	Pass   bool      `json:"pass"`
	Why    string    `json:"why,omitempty"`
}

// CalibrationStatus is the gate's answer.
type CalibrationStatus struct {
	Pass      bool        `json:"pass"`
	Source    string      `json:"source"`
	Split     string      `json:"split"`
	Tolerance Tolerance   `json:"tolerance"`
	Checks    []ToolCheck `json:"checks"`
}

// calibrationTools maps each real tool to the generator kinds that stand for it,
// with the weight of each kind. The weights are part of the fit.
var calibrationTools = []struct {
	tool    string
	kinds   []Kind
	weights []int
}{
	{"Read", []Kind{KindReadSmall, KindReadLarge}, []int{2, 1}},
	{"Grep", []Kind{KindGrep}, []int{1}},
	{"Bash", []Kind{KindBashSmall, KindBuildLog, KindTestVerbose, KindStackTrace}, []int{12, 1, 1, 2}},
}

// CheckCalibration compares the generator with the holdout targets and returns the gate.
func CheckCalibration(t Targets, tol Tolerance) CalibrationStatus {
	st := CalibrationStatus{Pass: true, Source: t.Source, Split: t.Split, Tolerance: tol}
	for _, ct := range calibrationTools {
		want, ok := t.Holdout[ct.tool]
		got := summarize(generated(ct.kinds, ct.weights, calibrationSeed, calibrationSamples))
		chk := ToolCheck{Tool: ct.tool, Target: want, Got: got, Pass: true}
		switch {
		case !ok || want.N == 0:
			chk.Pass, chk.Why = false, "no holdout target for this tool"
		default:
			var fails []string
			if !ratioOK(float64(got.MedianB), float64(want.MedianB), tol.SizeRatio) {
				fails = append(fails, fmt.Sprintf("median size %d B against target %d B", got.MedianB, want.MedianB))
			}
			if !ratioOK(float64(got.P90B), float64(want.P90B), tol.SizeRatio) {
				fails = append(fails, fmt.Sprintf("90th percentile %d B against target %d B", got.P90B, want.P90B))
			}
			if absDiff(got.ANSI, want.ANSI) > tol.ShareAbs {
				fails = append(fails, fmt.Sprintf("color-code share %.0f%% against target %.0f%%", got.ANSI*100, want.ANSI*100))
			}
			if absDiff(got.RepeatLn, want.RepeatLn) > tol.ShareAbs {
				fails = append(fails, fmt.Sprintf("repeated-line share %.0f%% against target %.0f%%", got.RepeatLn*100, want.RepeatLn*100))
			}
			if len(fails) > 0 {
				chk.Pass = false
				chk.Why = strings.Join(fails, "; ")
			}
		}
		if !chk.Pass {
			st.Pass = false
		}
		st.Checks = append(st.Checks, chk)
	}
	return st
}

func ratioOK(got, want, limit float64) bool {
	if want == 0 || got == 0 {
		return got == want
	}
	r := got / want
	if r < 1 {
		r = 1 / r
	}
	return r <= limit
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// CalibrateLogs reads every *.jsonl under dir and returns the two halves' aggregates.
// Nothing is written. dir is usually ~/.claude/projects.
func CalibrateLogs(dir string) (Targets, error) {
	t := Targets{
		Source: "one developer's Claude Code sessions (Windows); 210 session files",
		Split:  "files sorted by path; even index fits the generator, odd index is the holdout the gate checks",
	}
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped; the file count says how many were read
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return t, fmt.Errorf("could not read the session logs under %s: %w", dir, err)
	}
	sort.Strings(files)
	t.Files = len(files)
	if len(files) == 0 {
		return t, fmt.Errorf("no session files (*.jsonl) found under %s; point --calibrate at a folder of Claude Code session logs", dir)
	}
	fitBy := map[string][]string{}
	holdBy := map[string][]string{}
	for i, f := range files {
		per := readLog(f)
		dst := holdBy
		if i%2 == 0 {
			dst = fitBy
		}
		for tool, texts := range per {
			dst[tool] = append(dst[tool], texts...)
			t.Results += len(texts)
		}
	}
	t.Fit = map[string]Aggregate{}
	t.Holdout = map[string]Aggregate{}
	for _, ct := range calibrationTools {
		t.Fit[ct.tool] = summarize(fitBy[ct.tool])
		t.Holdout[ct.tool] = summarize(holdBy[ct.tool])
	}
	return t, nil
}

// readLog extracts tool results from one session file, keyed by tool name. Each
// result is matched to its tool through the tool_use id the assistant sent.
// Unreadable files give nothing.
func readLog(path string) map[string][]string {
	out := map[string][]string{}
	f, err := os.Open(path) //nolint:gosec // path is a session log the user chose to calibrate from
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	names := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var line struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				names[b.ID] = b.Name
			case "tool_result":
				if tool, ok := names[b.ToolUseID]; ok {
					out[tool] = append(out[tool], resultText(b.Content))
				}
			}
		}
	}
	return out
}

// resultText returns the text of a tool_result content: a string, or the text
// blocks of an array.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &arr) == nil {
		var b strings.Builder
		for _, a := range arr {
			if a.Type == "text" {
				b.WriteString(a.Text)
			}
		}
		return b.String()
	}
	return ""
}

// generated draws n outputs for a tool class: each kind is picked with its weight.
// The draw is seeded, so the same inputs give the same sample.
func generated(kinds []Kind, weights []int, seed uint64, n int) []string {
	total := 0
	for _, w := range weights {
		total += w
	}
	r := newRand(seed, "calibration", fmt.Sprint(kinds))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		x := r.IntN(total)
		k := kinds[0]
		for j, w := range weights {
			if x < w {
				k = kinds[j]
				break
			}
			x -= w
		}
		out = append(out, genOutput(r, k))
	}
	return out
}

// summarize computes only aggregates of a set of outputs.
func summarize(texts []string) Aggregate {
	a := Aggregate{N: len(texts)}
	if len(texts) == 0 {
		return a
	}
	sizes := make([]float64, 0, len(texts))
	ansi, lines, dup := 0, 0, 0
	for _, t := range texts {
		sizes = append(sizes, float64(len(t)))
		if strings.Contains(t, "\x1b[") {
			ansi++
		}
		seen := map[string]bool{}
		for _, l := range strings.Split(t, "\n") {
			lines++
			if seen[l] {
				dup++
			}
			seen[l] = true
		}
	}
	sort.Float64s(sizes)
	a.MedianB = int(Median(sizes))
	a.P90B = int(quantile(sizes, 0.9))
	a.ANSI = float64(ansi) / float64(len(texts))
	if lines > 0 {
		a.RepeatLn = float64(dup) / float64(lines)
	}
	return a
}

// CalibrationMarkdown renders the fit and the gate. It has no content and no paths.
func CalibrationMarkdown(t Targets, st CalibrationStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Calibration: %s\n\n", map[bool]string{true: "PASS", false: "FAIL"}[st.Pass])
	fmt.Fprintf(&b, "**Source:** %s (%d tool results). **Split:** %s.\n\n", t.Source, t.Results, t.Split)
	fmt.Fprintf(&b, "**Gate:** holdout half, tolerance ±%.0f%% on sizes, ±%.0f points on shares. Generated sample: %d outputs per tool, seed %d.\n\n",
		(st.Tolerance.SizeRatio-1)*100, st.Tolerance.ShareAbs*100, calibrationSamples, calibrationSeed)
	b.WriteString("| Tool | Holdout median / p90 B | Generated median / p90 B | Holdout color codes | Generated | Holdout repeated lines | Generated | Result |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---|\n")
	for _, c := range st.Checks {
		res := "pass"
		if !c.Pass {
			res = "FAIL: " + c.Why
		}
		fmt.Fprintf(&b, "| %s | %d / %d | %d / %d | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %s |\n",
			c.Tool, c.Target.MedianB, c.Target.P90B, c.Got.MedianB, c.Got.P90B,
			c.Target.ANSI*100, c.Got.ANSI*100, c.Target.RepeatLn*100, c.Got.RepeatLn*100, res)
	}
	b.WriteString("\nFit half (in-sample, for reference only):\n\n")
	b.WriteString("| Tool | N | Median / p90 B | Color codes | Repeated lines |\n|---|---:|---:|---:|---:|\n")
	for _, ct := range calibrationTools {
		a := t.Fit[ct.tool]
		fmt.Fprintf(&b, "| %s | %d | %d / %d | %.0f%% | %.0f%% |\n", ct.tool, a.N, a.MedianB, a.P90B, a.ANSI*100, a.RepeatLn*100)
	}
	return b.String()
}
