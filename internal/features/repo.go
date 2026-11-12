package features

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SizeBucket is the size of a repo by file count, a closed set.
type SizeBucket string

// The size buckets.
const (
	SizeS  SizeBucket = "S"  // under 200 files
	SizeM  SizeBucket = "M"  // under 2,000
	SizeL  SizeBucket = "L"  // under 20,000
	SizeXL SizeBucket = "XL" // 20,000 or more, or the scan hit its limit
)

// TestFramework is the test framework a repo uses, a closed set.
type TestFramework string

// The test frameworks the detector knows.
const (
	TestGo      TestFramework = "go_test"
	TestPytest  TestFramework = "pytest"
	TestJest    TestFramework = "jest"
	TestVitest  TestFramework = "vitest"
	TestMocha   TestFramework = "mocha"
	TestCargo   TestFramework = "cargo_test"
	TestJUnit   TestFramework = "junit"
	TestRSpec   TestFramework = "rspec"
	TestUnknown TestFramework = "unknown"
)

// LangShare is one language's share of a repo's source files, in whole percent.
type LangShare struct {
	Lang FileType `json:"lang"`
	Pct  int      `json:"pct"`
}

// Repo holds the bucketed features of a repo. Nothing in it names a file.
type Repo struct {
	Size          SizeBucket    `json:"size"`
	LangMix       []LangShare   `json:"lang_mix"` // top three, largest first
	TestFramework TestFramework `json:"test_framework"`
}

// Repo scan limits.
const (
	MaxRepoFiles = 20000
	maxPkgJSON   = 256 << 10
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"target": true, "dist": true, "build": true, "__pycache__": true, ".trove": true}

// RepoKey identifies a repo on this device without naming it: a truncated hash
// of the working directory under a per-install salt. The path is not kept.
func RepoKey(cwd, salt string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:8])
}

// ScanRepo buckets the repo under root. It stops at MaxRepoFiles files or when
// ctx is done, whichever comes first, and then reports what it saw (size XL if
// cut short). It never fails; an unreadable root gives an empty, unknown Repo.
func ScanRepo(ctx context.Context, root string) Repo {
	counts := map[FileType]int{}
	var files int
	cut := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil || files >= MaxRepoFiles {
			cut = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		files++
		if t := FileTypeOf(p); t != FileOther && t != FileUnknown && t != FileData && t != FileMD {
			counts[t]++
		}
		return nil
	})
	r := Repo{Size: sizeBucket(files, cut), LangMix: langMix(counts), TestFramework: detectFramework(root)}
	return r
}

func sizeBucket(files int, cut bool) SizeBucket {
	switch {
	case cut || files >= MaxRepoFiles:
		return SizeXL
	case files >= 2000:
		return SizeL
	case files >= 200:
		return SizeM
	}
	return SizeS
}

func langMix(counts map[FileType]int) []LangShare {
	total := 0
	for _, n := range counts {
		total += n
	}
	out := []LangShare{}
	if total == 0 {
		return out
	}
	for t, n := range counts {
		out = append(out, LangShare{Lang: t, Pct: n * 100 / total})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pct != out[j].Pct {
			return out[i].Pct > out[j].Pct
		}
		return out[i].Lang < out[j].Lang
	})
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// detectFramework looks for marker files in the repo root. It reads package.json
// in memory to tell the JavaScript runners apart and keeps only the result.
func detectFramework(root string) TestFramework {
	switch {
	case exists(root, "go.mod"):
		return TestGo
	case exists(root, "pytest.ini"), exists(root, "conftest.py"), exists(root, "tox.ini"):
		return TestPytest
	case exists(root, "Cargo.toml"):
		return TestCargo
	case exists(root, ".rspec"):
		return TestRSpec
	case exists(root, "pom.xml"), exists(root, "build.gradle"), exists(root, "build.gradle.kts"):
		return TestJUnit
	}
	if f, err := os.Open(filepath.Join(root, "package.json")); err == nil { //nolint:gosec // root is the user's own repo; the file name is fixed
		defer func() { _ = f.Close() }()
		buf := make([]byte, maxPkgJSON)
		n, _ := f.Read(buf)
		var pj struct {
			Deps    map[string]string `json:"dependencies"`
			DevDeps map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(buf[:n], &pj) == nil {
			for _, m := range []map[string]string{pj.DevDeps, pj.Deps} {
				for _, c := range []struct {
					dep string
					fw  TestFramework
				}{{"vitest", TestVitest}, {"jest", TestJest}, {"mocha", TestMocha}} {
					if _, ok := m[c.dep]; ok {
						return c.fw
					}
				}
			}
		}
	}
	if exists(root, "pyproject.toml") {
		if b, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil && strings.Contains(string(b), "pytest") { //nolint:gosec // root is the user's own repo; the file name is fixed
			return TestPytest
		}
	}
	return TestUnknown
}
