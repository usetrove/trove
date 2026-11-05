package features

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// enums lists every named string type a feature may use, with its fixed values.
// A feature field of any other string type, or a bare string, fails the test.
var enums = map[reflect.Type][]string{
	reflect.TypeOf(FileType("")): {"go", "python", "javascript", "typescript", "rust", "java", "ruby", "c_cpp",
		"markdown", "data", "shell", "other", "unknown"},
	reflect.TypeOf(SizeBucket("")):    {"S", "M", "L", "XL"},
	reflect.TypeOf(TestFramework("")): {"go_test", "pytest", "jest", "vitest", "mocha", "cargo_test", "junit", "rspec", "unknown"},
}

func checkType(t *testing.T, path string, ty reflect.Type) {
	t.Helper()
	switch ty.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint64, reflect.Float64:
	case reflect.String:
		if _, ok := enums[ty]; !ok {
			t.Errorf("%s has type %s: a string field must be a closed enum listed in enums", path, ty)
		}
	case reflect.Slice:
		checkType(t, path+"[]", ty.Elem())
	case reflect.Struct:
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if !f.IsExported() {
				t.Errorf("%s.%s is unexported: feature types must be plain data", path, f.Name)
				continue
			}
			checkType(t, path+"."+f.Name, f.Type)
		}
	default:
		t.Errorf("%s has kind %s, which a feature may not use", path, ty.Kind())
	}
}

// TestNoFeatureCanHoldText is the acceptance test: no feature type has a field
// that could carry text from the content.
func TestNoFeatureCanHoldText(t *testing.T) {
	for _, v := range []any{Result{}, SessionFeatures{}, Repo{}} {
		ty := reflect.TypeOf(v)
		checkType(t, ty.Name(), ty)
	}
}

func TestEnumConstantsAreInTheFixedSets(t *testing.T) {
	for _, tc := range []struct {
		v  string
		ty reflect.Type
	}{
		{string(FileGo), reflect.TypeOf(FileType(""))}, {string(FileUnknown), reflect.TypeOf(FileType(""))},
		{string(SizeXL), reflect.TypeOf(SizeBucket(""))}, {string(TestUnknown), reflect.TypeOf(TestFramework(""))},
	} {
		found := false
		for _, e := range enums[tc.ty] {
			found = found || e == tc.v
		}
		if !found {
			t.Errorf("%q missing from the fixed set for %s", tc.v, tc.ty)
		}
	}
}

const sentinel = "SENTINEL_SECRET_f00d"

func TestToolResultFeatures(t *testing.T) {
	var pass strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&pass, "--- PASS: TestThing%d (0.00s)\n", i)
	}
	tests := []struct {
		name string
		path string
		out  string
		want func(Result) string
	}{
		{"empty", "", "", func(r Result) string {
			if r.SizeBytes != 0 || r.Lines != 0 || r.HasError || r.FileType != FileUnknown {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
		{"duplicates", "a.go", "x\nx\nx\ny\n", func(r Result) string {
			if r.DupLineRatio != 0.5 || r.FileType != FileGo || r.Lines != 4 {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
		{"error", "", "all fine\nerror: cannot open\n", func(r Result) string {
			if !r.HasError || r.HasStackTrace {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
		{"python trace", "", "Traceback (most recent call last):\n  File \"a.py\", line 3, in f\nValueError: x\n", func(r Result) string {
			if !r.HasError || !r.HasStackTrace {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
		{"all pass", "", pass.String(), func(r Result) string {
			if !r.HasTests || r.PassShare != 1 || r.HasError {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
		{"mixed", "", "--- PASS: A\n--- PASS: B\n--- PASS: C\n--- FAIL: D (0.00s)\n", func(r Result) string {
			if !r.HasTests || r.PassShare != 0.75 || !r.HasError {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
		{"no tests", "x.md", "just prose\n", func(r Result) string {
			if r.HasTests || r.PassShare != 0 || r.FileType != FileMD {
				return fmt.Sprintf("%+v", r)
			}
			return ""
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if msg := tc.want(ToolResult(tc.path, []byte(tc.out))); msg != "" {
				t.Errorf("unexpected features: %s", msg)
			}
		})
	}
}

func TestToolResultIsBoundedAndFast(t *testing.T) {
	big := []byte(strings.Repeat("line of output that repeats\n", 200000)) // ~5.6 MB
	start := time.Now()
	r := ToolResult("", big)
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Errorf("extraction took %v for %d bytes; the budget is 250ms at the scan limits", d, len(big))
	}
	if !r.Truncated || r.SizeBytes != len(big) || r.Lines > MaxScanLines {
		t.Errorf("limits not applied: %+v", r)
	}
	typical := []byte(strings.Repeat("ok  \tpkg/x\t0.123s\n", 400))
	start = time.Now()
	ToolResult("", typical)
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("a typical output took %v", d)
	}
}

func BenchmarkToolResult(b *testing.B) {
	out := []byte(strings.Repeat("ok  \tpkg/x\t0.123s\n--- PASS: TestX (0.00s)\n", 300))
	b.SetBytes(int64(len(out)))
	for i := 0; i < b.N; i++ {
		ToolResult("x.go", out)
	}
}

// TestFeaturesContainNoContent plants a sentinel in every kind of content and
// checks it appears nowhere in the serialized features.
func TestFeaturesContainNoContent(t *testing.T) {
	out := []byte(sentinel + "\nerror: " + sentinel + "\n  at " + sentinel + ".js:3\n  at x.js:4\n--- FAIL: " + sentinel + "\n")
	res := ToolResult("/home/u/"+sentinel+".go", out)
	s := NewSession()
	s.Read(HashPath("/home/u/" + sentinel + ".go"))
	s.Call(HashPath("run "+sentinel), true)
	s.Add(res)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, sentinel+".go"), []byte("package x // "+sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"`+sentinel+`","devDependencies":{"jest":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := ScanRepo(context.Background(), root)

	for name, v := range map[string]any{"result": res, "session": s.Snapshot(), "repo": repo, "key": RepoKey(root, sentinel)} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), sentinel) || strings.Contains(string(b), root) {
			t.Errorf("%s features contain content: %s", name, b)
		}
	}
	if repo.TestFramework != TestJest {
		t.Errorf("framework = %s, want jest", repo.TestFramework)
	}
}

func TestSessionFeatures(t *testing.T) {
	s := NewSession()
	a, b := HashPath("a"), HashPath("b")
	s.Read(a)
	s.Read(a)
	s.Read(b)
	s.Edit(a)
	s.Scout()
	s.Compaction()
	s.Call(1, true)
	s.Call(1, false) // retry
	s.Call(2, true)
	s.Call(3, false) // different call: not a retry
	s.Add(Result{DupLineRatio: 0.5, HasError: true, SizeBytes: 9000})
	s.Add(Result{DupLineRatio: 0.1, HasStackTrace: true})
	got := s.Snapshot()
	want := SessionFeatures{FilesRead: 2, FilesEdited: 1, RepeatReads: 1, ScoutCalls: 1, Compactions: 1, Retries: 1,
		ToolResults: 2, AvgDupRatio: 0.3, ErrorShare: 0.5, StackShare: 0.5, LargeResults: 1}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if (NewSession().Snapshot() != SessionFeatures{}) {
		t.Error("an empty session should give zero features")
	}
}

func TestScanRepo(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x")
	for i := 0; i < 3; i++ {
		write(fmt.Sprintf("a%d.go", i), "package a")
	}
	write("s.py", "x=1")
	write("node_modules/dep/index.js", "x")
	write(".git/config", "x")
	r := ScanRepo(context.Background(), root)
	if r.Size != SizeS || r.TestFramework != TestGo {
		t.Errorf("got %+v", r)
	}
	if len(r.LangMix) != 2 || r.LangMix[0] != (LangShare{FileGo, 75}) || r.LangMix[1] != (LangShare{FilePython, 25}) {
		t.Errorf("lang mix = %+v (node_modules and .git must be skipped)", r.LangMix)
	}
	empty := ScanRepo(context.Background(), filepath.Join(root, "missing"))
	if empty.TestFramework != TestUnknown || len(empty.LangMix) != 0 {
		t.Errorf("a missing root should give an unknown, empty repo: %+v", empty)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := ScanRepo(ctx, root); got.Size != SizeXL {
		t.Errorf("a cancelled scan reports XL, got %s", got.Size)
	}
}

func TestRepoKey(t *testing.T) {
	a, b := RepoKey("/work/a", "salt"), RepoKey("/work/b", "salt")
	if a == b || len(a) != 16 || a != RepoKey("/work/a/", "salt") || a == RepoKey("/work/a", "other") {
		t.Errorf("repo keys wrong: %s %s", a, b)
	}
}
