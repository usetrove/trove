// Package features computes content-derived features on the device.
//
// A feature is a number, a boolean or a value from a small fixed set. No
// feature can hold text from the content it was derived from: tool output,
// code, file paths and commands are read in memory, reduced to these values and
// dropped (CLAUDE.md rules 7 and 12). TestNoFeatureCanHoldText walks every
// exported feature type and fails if a field could carry a string that is not a
// closed category, and the sentinel tests check that nothing planted in the
// content reaches the database or the JSON output.
//
// Extraction is bounded: at most MaxScanBytes of one output and MaxScanLines
// lines are looked at, so the cost is small and fixed whatever the output size.
// It is meant to run inside a hook, so it never fails: bad input gives a zero
// result. See docs/features.md.
package features

import (
	"hash/fnv"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/usetrove/trove/internal/optimize"
)

// Scan limits. They keep the cost of one extraction fixed.
const (
	MaxScanBytes = 256 << 10
	MaxScanLines = 4000
)

// FileType is the kind of file a tool result is about. It is a closed set.
type FileType string

// The file types. Anything not listed is FileOther; no path or extension is kept.
const (
	FileGo      FileType = "go"
	FilePython  FileType = "python"
	FileJS      FileType = "javascript"
	FileTS      FileType = "typescript"
	FileRust    FileType = "rust"
	FileJava    FileType = "java"
	FileRuby    FileType = "ruby"
	FileC       FileType = "c_cpp"
	FileMD      FileType = "markdown"
	FileData    FileType = "data" // json, yaml, toml, csv
	FileShell   FileType = "shell"
	FileOther   FileType = "other"
	FileUnknown FileType = "unknown" // the tool result has no file (a command, say)
)

var extTypes = map[string]FileType{
	".go": FileGo, ".py": FilePython, ".js": FileJS, ".jsx": FileJS, ".mjs": FileJS, ".cjs": FileJS,
	".ts": FileTS, ".tsx": FileTS, ".rs": FileRust, ".java": FileJava, ".kt": FileJava,
	".rb": FileRuby, ".c": FileC, ".h": FileC, ".cc": FileC, ".cpp": FileC, ".hpp": FileC,
	".md": FileMD, ".json": FileData, ".yaml": FileData, ".yml": FileData, ".toml": FileData, ".csv": FileData,
	".sh": FileShell, ".bash": FileShell,
}

// FileTypeOf maps a path to a file type by extension. The path is not kept.
func FileTypeOf(path string) FileType {
	if path == "" {
		return FileUnknown
	}
	if t, ok := extTypes[strings.ToLower(filepath.Ext(path))]; ok {
		return t
	}
	return FileOther
}

// Result holds the features of one tool result.
type Result struct {
	SizeBytes     int      `json:"size_bytes"`
	Lines         int      `json:"lines"`
	DupLineRatio  float64  `json:"dup_line_ratio"` // repeated non-blank lines / non-blank lines, 0 to 1, 2 decimals
	HasError      bool     `json:"has_error"`
	HasStackTrace bool     `json:"has_stack_trace"`
	HasTests      bool     `json:"has_tests"`  // the output has test-result lines
	PassShare     float64  `json:"pass_share"` // passing / (passing + failing) test lines; 0 when HasTests is false
	FileType      FileType `json:"file_type"`
	Truncated     bool     `json:"truncated"` // the scan limits cut the output short
}

var (
	passRe  = regexp.MustCompile(`^\s*(?:--- PASS|ok\s+\S+\s|PASS\b|[✓✔] |test \S+ \.\.\. ok|\S+\.py \.+|PASSED\b)`)
	frameRe = regexp.MustCompile(`^\s+at .+:\d+|\.go:\d+|File ".+", line \d+|^\s+\S+\.(?:rs|java|kt|py|js|ts):\d+`)
)

// ToolResult extracts the features of one tool result. path is the file the
// call was about, if any; only its extension is looked at. It never fails.
func ToolResult(path string, output []byte) Result {
	r := Result{SizeBytes: len(output), FileType: FileTypeOf(path)}
	if len(output) > MaxScanBytes {
		output, r.Truncated = output[:MaxScanBytes], true
	}
	seen := make(map[uint64]struct{}, 256)
	var nonBlank, dups, frames, pass, fail int
	h := fnv.New64a()
	rest := string(output)
	for rest != "" {
		if r.Lines >= MaxScanLines {
			r.Truncated = true
			break
		}
		var line string
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			line, rest = rest, ""
		}
		r.Lines++
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonBlank++
		h.Reset()
		_, _ = h.Write([]byte(trimmed))
		sum := h.Sum64()
		if _, ok := seen[sum]; ok {
			dups++
		} else {
			seen[sum] = struct{}{}
		}
		switch {
		case passRe.MatchString(line):
			pass++
		case optimize.IsFailureLine(line):
			r.HasError = true
			if strings.Contains(line, "FAIL") || strings.Contains(line, "✗") || strings.Contains(line, "×") {
				fail++
			}
			if strings.Contains(line, "Traceback (most recent call last)") || strings.Contains(line, "panic:") {
				r.HasStackTrace = true
			}
		}
		if frameRe.MatchString(line) {
			frames++
		}
	}
	if frames >= 2 {
		r.HasStackTrace = true
	}
	if nonBlank > 0 {
		r.DupLineRatio = round2(float64(dups) / float64(nonBlank))
	}
	if pass+fail > 0 {
		r.HasTests = true
		r.PassShare = round2(float64(pass) / float64(pass+fail))
	}
	return r
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
