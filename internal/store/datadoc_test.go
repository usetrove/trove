package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

// docs/data.md is the published inventory of what Trove stores. This test keeps it true: every table and
// column in the schema must be described, every documented one must exist, and every table must state a
// retention that matches retention.go.

type docTable struct {
	columns   map[string]string // column -> meaning
	retention string
}

func parseDataDoc(t *testing.T) map[string]*docTable {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "data.md"))
	if err != nil {
		t.Fatalf("docs/data.md is required: %v", err)
	}
	head := regexp.MustCompile("^### `([a-z_]+)`$")
	row := regexp.MustCompile("^[|] `([a-z_0-9]+)` [|] (.+) [|]$")
	out := map[string]*docTable{}
	var cur *docTable
	inDB := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "## "):
			inDB = line == "## The local database"
			cur = nil
		case inDB && head.MatchString(line):
			cur = &docTable{columns: map[string]string{}}
			out[head.FindStringSubmatch(line)[1]] = cur
		case cur != nil && strings.HasPrefix(line, "**Retention:**"):
			cur.retention = strings.TrimSpace(strings.TrimPrefix(line, "**Retention:**"))
		case cur != nil && row.MatchString(line):
			m := row.FindStringSubmatch(line)
			cur.columns[m[1]] = m[2]
		}
	}
	return out
}

func TestDataDocMatchesSchema(t *testing.T) {
	db := openTemp(t)
	doc := parseDataDoc(t)
	aged, kept := RetentionDecisions()
	for _, tbl := range schemaTables(t, db) {
		d, ok := doc[tbl]
		if !ok {
			t.Errorf("table %q is not described in docs/data.md: add a section with every column's meaning and its retention", tbl)
			continue
		}
		var cols []string
		for _, c := range columns(t, db, tbl) {
			cols = append(cols, c.name)
		}
		sort.Strings(cols)
		for _, c := range cols {
			meaning, ok := d.columns[c]
			if !ok {
				t.Errorf("docs/data.md: %s.%s is not described", tbl, c)
			} else if len(strings.TrimSpace(meaning)) < 4 {
				t.Errorf("docs/data.md: %s.%s has no real meaning (%q)", tbl, c, meaning)
			}
		}
		for c := range d.columns {
			found := false
			for _, have := range cols {
				found = found || have == c
			}
			if !found {
				t.Errorf("docs/data.md describes %s.%s, which is not in the schema", tbl, c)
			}
		}
		if _, isAged := aged[tbl]; isAged && !strings.HasPrefix(d.retention, "deleted after `retention_days`") {
			t.Errorf("docs/data.md: %s is deleted by age in retention.go, but its Retention line is %q", tbl, d.retention)
		}
		if _, isKept := kept[tbl]; isKept && !strings.HasPrefix(d.retention, "kept") {
			t.Errorf("docs/data.md: %s is kept in retention.go, but its Retention line is %q", tbl, d.retention)
		}
	}
	have := map[string]bool{}
	for _, tbl := range schemaTables(t, db) {
		have[tbl] = true
	}
	for tbl := range doc {
		if !have[tbl] {
			t.Errorf("docs/data.md describes table %q, which is not in the schema", tbl)
		}
	}
}

func TestDataDocStatesTheDefaultRetention(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "data.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("default **%d days**", config.Default().RetentionDays)
	if !strings.Contains(string(b), want) {
		t.Errorf("docs/data.md must state the default retention as %q", want)
	}
}
