package share

import (
	"fmt"
	"image/color"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// Card size: 1200 x 630 is the 1.91:1 shape X and most feeds show uncropped.
const (
	Width  = 1200
	Height = 630
)

// Theme picks the card palette. Each card paints its own solid background, so it
// reads the same whether it is posted on a light or a dark page.
type Theme string

// Themes.
const (
	Dark  Theme = "dark"
	Light Theme = "light"
)

type palette struct {
	bg, panel, text, muted, rule, amber, green color.RGBA
}

func rgb(hex uint32) color.RGBA {
	return color.RGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 255} //nolint:gosec // truncation to one byte per channel is intended
}

// Colors carry meaning: amber is waste, green is savings. Every text color is
// at least 4.5:1 against the surface it sits on.
var palettes = map[Theme]palette{
	Dark:  {bg: rgb(0x0E1217), panel: rgb(0x171D25), text: rgb(0xF3F5F7), muted: rgb(0x9AA6B5), rule: rgb(0x2A323D), amber: rgb(0xF2A83B), green: rgb(0x46D39A)},
	Light: {bg: rgb(0xF6F3EC), panel: rgb(0xFFFFFF), text: rgb(0x14181D), muted: rgb(0x566170), rule: rgb(0xD9D4C8), amber: rgb(0x9A5B00), green: rgb(0x0A7050)},
}

// rect is a filled rectangle with rounded corners (radius 0 for square).
type rect struct {
	x, y, w, h, r float64
	fill          color.RGBA
}

// text is one run of text; y is the baseline.
type text struct {
	x, y, size float64
	bold       bool
	end        bool // right-aligned at x
	fill       color.RGBA
	s          string
}

type scene struct {
	bg    color.RGBA
	rects []rect
	texts []text
	title string
	desc  string
}

// fonts are parsed once.
var (
	fontOnce         sync.Once
	regular, bold    *opentype.Font
	fontErr          error
	faceMu           sync.Mutex
	faceCache        = map[faceKey]font.Face{}
	errFontsNotReady = fmt.Errorf("the built-in fonts could not be loaded")
)

type faceKey struct {
	size float64
	bold bool
}

func loadFonts() error {
	fontOnce.Do(func() {
		if regular, fontErr = opentype.Parse(goregular.TTF); fontErr != nil {
			return
		}
		bold, fontErr = opentype.Parse(gobold.TTF)
	})
	if fontErr != nil {
		return fmt.Errorf("%w: %v", errFontsNotReady, fontErr)
	}
	return nil
}

func face(size float64, isBold bool) (font.Face, error) {
	if err := loadFonts(); err != nil {
		return nil, err
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	k := faceKey{size, isBold}
	if f, ok := faceCache[k]; ok {
		return f, nil
	}
	src := regular
	if isBold {
		src = bold
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	faceCache[k] = f
	return f, nil
}

// textWidth is the width in pixels of s at size, with the built-in font.
func textWidth(s string, size float64, isBold bool) float64 {
	f, err := face(size, isBold)
	if err != nil {
		return float64(len(s)) * size * 0.62
	}
	return float64(font.MeasureString(f, s).Ceil())
}

// fit returns the largest size, from max down to min, at which s fits in width
// with 8% to spare (viewers draw SVG text with their own fonts).
func fit(s string, hi, lo, width float64, isBold bool) float64 {
	for size := hi; size > lo; size -= 2 {
		if textWidth(s, size, isBold)*1.08 <= width {
			return size
		}
	}
	return lo
}

// build lays out the card. Both renderers draw exactly this scene.
func build(c Card, th Theme) scene {
	p, ok := palettes[th]
	if !ok {
		p = palettes[Dark]
	}
	const margin = 64.0
	sc := scene{bg: p.bg,
		title: "Trove summary for " + c.Period,
		desc:  fmt.Sprintf("Estimated spend %s, waste found %s, saved %s. Biggest leak: %s.", c.spend(), c.waste(), c.saved(), c.leak()),
	}
	add := func(t text) { sc.texts = append(sc.texts, t) }

	add(text{x: margin, y: 100, size: 36, bold: true, fill: p.text, s: "trove"})
	add(text{x: Width - margin, y: 98, size: 28, end: true, fill: p.muted, s: c.Period})
	sc.rects = append(sc.rects, rect{x: margin, y: 126, w: Width - 2*margin, h: 2, fill: p.rule})

	const (
		gap, top, panelH = 24.0, 164.0, 262.0
		panelW           = (Width - 2*margin - 2*gap) / 3 // three panels spanning the margins
		inner            = panelW - 56
	)
	type col struct {
		label, value, sub1, sub2 string
		valueColor               color.RGBA
	}
	cols := []col{
		{"SPEND", est(c.SpendUSD), "at list prices", "estimated", p.text},
		{"WASTE FOUND", c.wasteValue(), c.wasteSub(), "estimated", p.amber},
		{"SAVED", c.savedValue(), "by optimizers that were on", "estimated", p.green},
	}
	if c.SavedUSD < 0.01 { // nothing to label as an estimate
		cols[2].valueColor, cols[2].sub2 = p.muted, ""
	}
	if c.WasteUSD <= 0 {
		cols[1].valueColor, cols[1].sub2 = p.muted, ""
	}
	for i, k := range cols {
		x := margin + float64(i)*(panelW+gap)
		sc.rects = append(sc.rects, rect{x: x, y: top, w: panelW, h: panelH, r: 20, fill: p.panel})
		add(text{x: x + 28, y: top + 54, size: 20, fill: p.muted, s: k.label})
		add(text{x: x + 28, y: top + 156, size: fit(k.value, 84, 32, inner, true), bold: true, fill: k.valueColor, s: k.value})
		add(text{x: x + 28, y: top + 198, size: fit(k.sub1, 22, 14, inner, false), fill: p.muted, s: k.sub1})
		if k.sub2 != "" {
			add(text{x: x + 28, y: top + 230, size: 20, bold: true, fill: p.muted, s: k.sub2})
		}
	}

	add(text{x: margin, y: 482, size: 20, fill: p.muted, s: "BIGGEST LEAK"})
	leakColor := p.text
	if c.TopLeak == "" {
		leakColor = p.muted
	}
	add(text{x: margin, y: 532, size: fit(c.leak(), 44, 24, Width-2*margin, true), bold: true, fill: leakColor, s: c.leak()})

	sc.rects = append(sc.rects, rect{x: margin, y: 566, w: Width - 2*margin, h: 2, fill: p.rule})
	add(text{x: margin, y: 604, size: 20, fill: p.muted, s: "Dollar figures are estimates from list prices, not invoices."})
	add(text{x: Width - margin, y: 604, size: 20, end: true, fill: p.muted, s: "Made locally. Nothing uploaded."})
	return sc
}

func (c Card) wasteValue() string {
	if c.WasteUSD <= 0 {
		return "none yet"
	}
	return est(c.WasteUSD)
}

func (c Card) wasteSub() string {
	if c.WasteUSD <= 0 {
		return "nothing found so far"
	}
	if c.WastePercent > 0 {
		return reportPercent(c.WastePercent) + " of spend, at least"
	}
	return "at least this much"
}

func (c Card) savedValue() string {
	if c.SavedUSD < 0.01 {
		return "none yet"
	}
	return est(c.SavedUSD)
}
