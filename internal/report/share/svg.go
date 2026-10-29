package share

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image/color"
	"strings"
)

// fontStack is a clean sans-serif on every platform.
const fontStack = `Inter, 'SF Pro Display', 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif`

func hex(c color.RGBA) string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// SVG renders the card as an SVG document. It has its own solid background, so
// it looks right on a light or a dark page.
func SVG(c Card, th Theme) string {
	sc := build(c, th)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="t d">`+"\n", Width, Height, Width, Height)
	fmt.Fprintf(&b, "<title id=\"t\">%s</title>\n<desc id=\"d\">%s</desc>\n", esc(sc.title), esc(sc.desc))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="%s"/>`+"\n", Width, Height, hex(sc.bg))
	for _, r := range sc.rects {
		round := ""
		if r.r > 0 {
			round = fmt.Sprintf(` rx="%g"`, r.r)
		}
		fmt.Fprintf(&b, `<rect x="%g" y="%g" width="%g" height="%g"%s fill="%s"/>`+"\n", r.x, r.y, r.w, r.h, round, hex(r.fill))
	}
	for _, t := range sc.texts {
		weight, anchor := "400", ""
		if t.bold {
			weight = "700"
		}
		if t.end {
			anchor = ` text-anchor="end"`
		}
		fmt.Fprintf(&b, `<text x="%g" y="%g" font-family="%s" font-size="%g" font-weight="%s"%s fill="%s">%s</text>`+"\n",
			t.x, t.y, fontStack, t.size, weight, anchor, hex(t.fill), esc(t.s))
	}
	b.WriteString("</svg>\n")
	return b.String()
}
