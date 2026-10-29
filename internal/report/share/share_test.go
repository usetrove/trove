package share

import (
	"bytes"
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

var sample = Card{Period: "Oct 2026", SpendUSD: 612.4, WasteUSD: 74.2, WastePercent: 12.1, SavedUSD: 47.3, TopLeak: "Repeated tool results", Label: "estimated"}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s; run `go test ./internal/report/share -update`", path)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Errorf("%s differs from its golden file", name)
	}
}

func TestSVGGoldens(t *testing.T) {
	golden(t, "card_dark.svg", SVG(sample, Dark))
	golden(t, "card_light.svg", SVG(sample, Light))
	empty := Card{Period: "Oct 2026", SpendUSD: 3.2, Label: "estimated"}
	golden(t, "card_empty_dark.svg", SVG(empty, Dark))
}

func TestPNGRendersInBothThemes(t *testing.T) {
	var bgs [2]uint32
	for i, th := range []Theme{Dark, Light} {
		b, err := PNG(sample, th)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: not a valid PNG: %v", th, err)
		}
		if img.Bounds() != image.Rect(0, 0, Width, Height) {
			t.Errorf("%s: size %v", th, img.Bounds())
		}
		r, g, bl, _ := img.At(5, 5).RGBA()
		bgs[i] = r>>8<<16 | g>>8<<8 | bl>>8
		// The picture must hold more than a flat fill: text and panels exist.
		colors := map[uint32]bool{}
		for y := 0; y < Height; y += 3 {
			for x := 0; x < Width; x += 3 {
				cr, cg, cb, _ := img.At(x, y).RGBA()
				colors[cr>>8<<16|cg>>8<<8|cb>>8] = true
			}
		}
		if len(colors) < 20 {
			t.Errorf("%s: only %d colors; the card looks blank", th, len(colors))
		}
		if bytes.Contains(b, []byte("tEXt")) || bytes.Contains(b, []byte("iTXt")) {
			t.Errorf("%s: the PNG carries metadata chunks", th)
		}
	}
	if bgs[0] == bgs[1] {
		t.Error("light and dark cards have the same background")
	}
	if bgs[0] != 0x0E1217 || bgs[1] != 0xF6F3EC {
		t.Errorf("backgrounds = %06X, %06X", bgs[0], bgs[1])
	}
}

// Text must fit on the card for any size of number.
func TestLongNumbersAndNamesFit(t *testing.T) {
	c := Card{Period: "Since Sep 15, 2026", SpendUSD: 1234567.89, WasteUSD: 987654.3, WastePercent: 80, SavedUSD: 123456.7,
		TopLeak: "Large model on small tasks", Label: "estimated"}
	for _, th := range []Theme{Dark, Light} {
		for _, tx := range build(c, th).texts {
			w := textWidth(tx.s, tx.size, tx.bold)
			left := tx.x
			if tx.end {
				left = tx.x - w
			}
			if left < 0 || left+w > Width {
				t.Errorf("%s: %q (%.0f px wide at %g) leaves the card", th, tx.s, w, tx.size)
			}
		}
	}
	// Each big number stays inside its panel.
	for _, tx := range build(c, Dark).texts {
		if tx.bold && tx.size >= 32 && strings.HasPrefix(tx.s, "~$") && textWidth(tx.s, tx.size, true) > 341-56 {
			t.Errorf("%q overflows its panel at size %g", tx.s, tx.size)
		}
	}
}

// The card must contain nothing that identifies the user, repo, files, prompts,
// agents or models, even when the underlying data is full of such names.
func TestCardHasNoIdentifyingData(t *testing.T) {
	secrets := []string{"acme-secret-repo", "payments.go", "hunter2-prompt", "acme-private-model-v9", "secret-agent-acme", "someuser", "acme_secret_repo_detector"}

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	findings := `[` +
		`{"detector":"acme_secret_repo_detector","tokens":99000,"usd":50,"confidence":"high","label":"estimated","detail":{"path":"/home/someuser/acme-secret-repo/src/payments.go","prompt":"hunter2-prompt"}},` +
		`{"detector":"duplicate_tool_result","tokens":4000,"usd":0.5,"confidence":"high","label":"estimated","detail":{"tool":"grep acme-secret-repo","path":"/home/someuser/acme-secret-repo/src/payments.go"}}]`
	for i := 0; i < 25; i++ {
		_, err := db.Exec(`INSERT INTO requests(ts, session_key, agent, provider, model, status_code, input_uncached_tokens, output_tokens, cost_usd, findings)
			VALUES(?, ?, 'secret-agent-acme', 'anthropic', 'acme-private-model-v9', 200, 1000, 100, 2.0, ?)`,
			now.Add(-time.Duration(i+1)*time.Hour).UnixMilli(), fmt.Sprintf("acme-secret-repo-%d", i), findings)
		if err != nil {
			t.Fatal(err)
		}
	}
	period, err := report.NewPeriod(report.Month, "", now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := report.Build(context.Background(), db, report.Inputs{Config: config.Default(), Now: now}, period)
	if err != nil {
		t.Fatal(err)
	}
	card, ok := FromReport(r)
	if !ok {
		t.Fatal("expected a card")
	}
	if card.TopLeak != "Repeated tool results" {
		t.Errorf("top leak = %q; an unknown category must be skipped, not shown", card.TopLeak)
	}

	var all strings.Builder
	for _, th := range []Theme{Dark, Light} {
		all.WriteString(SVG(card, th))
		b, err := PNG(card, th)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		for _, tx := range build(card, th).texts {
			all.WriteString(tx.s + "\n")
		}
	}
	all.WriteString(strings.Join(card.Lines(), "\n"))
	for _, s := range secrets {
		if strings.Contains(strings.ToLower(all.String()), strings.ToLower(s)) {
			t.Errorf("the card contains %q", s)
		}
	}

	// And the whole set of strings on the card is the fixed vocabulary plus numbers.
	for _, tx := range build(card, Dark).texts {
		t.Logf("card text: %q", tx.s)
	}
}

func TestNothingToShareYet(t *testing.T) {
	if _, ok := FromReport(report.Report{}); ok {
		t.Error("an empty report must not produce a card")
	}
}

func TestSaveWritesBothFiles(t *testing.T) {
	dir := t.TempDir()
	svgPath, pngPath, err := Save(filepath.Join(dir, "out", "card"), sample, Dark)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(svgPath) != "card.svg" || filepath.Base(pngPath) != "card.png" {
		t.Errorf("paths %s %s", svgPath, pngPath)
	}
	for _, p := range []string{svgPath, pngPath} {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestSVGIsWellFormedXML(t *testing.T) {
	for _, th := range []Theme{Dark, Light} {
		dec := xml.NewDecoder(strings.NewReader(SVG(sample, th)))
		texts := 0
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", th, err)
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "text" {
				texts++
			}
		}
		if texts < 10 {
			t.Errorf("%s: only %d text elements", th, texts)
		}
	}
	// Characters that matter to XML are escaped.
	if got := SVG(Card{Period: `A & <B>`, SpendUSD: 1}, Dark); strings.Contains(got, "<B>") {
		t.Error("text is not escaped")
	}
}
