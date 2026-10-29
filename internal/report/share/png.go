package share

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// PNG renders the card as a Width x Height PNG with the built-in Go fonts. It
// draws the same scene as SVG. The file carries no metadata.
func PNG(c Card, th Theme) ([]byte, error) {
	sc := build(c, th)
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), image.NewUniform(sc.bg), image.Point{}, draw.Src)
	for _, r := range sc.rects {
		fillRoundRect(img, r)
	}
	for _, t := range sc.texts {
		f, err := face(t.size, t.bold)
		if err != nil {
			return nil, err
		}
		x := t.x
		if t.end {
			x -= float64(font.MeasureString(f, t.s).Ceil())
		}
		d := &font.Drawer{Dst: img, Src: image.NewUniform(t.fill), Face: f, Dot: fixed.P(int(math.Round(x)), int(math.Round(t.y)))}
		d.DrawString(t.s)
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fillRoundRect fills r with anti-aliased rounded corners, using the signed
// distance to the rounded rectangle as pixel coverage.
func fillRoundRect(img *image.RGBA, r rect) {
	x0, y0 := int(math.Floor(r.x)), int(math.Floor(r.y))
	x1, y1 := int(math.Ceil(r.x+r.w)), int(math.Ceil(r.y+r.h))
	cx, cy := r.x+r.w/2, r.y+r.h/2
	hw, hh := r.w/2-r.r, r.h/2-r.r
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			px := math.Abs(float64(x)+0.5-cx) - hw
			py := math.Abs(float64(y)+0.5-cy) - hh
			d := math.Hypot(math.Max(px, 0), math.Max(py, 0)) + math.Min(math.Max(px, py), 0) - r.r
			cov := math.Min(math.Max(0.5-d, 0), 1)
			if cov > 0 {
				blend(img, x, y, r.fill, cov)
			}
		}
	}
}

func blend(img *image.RGBA, x, y int, c color.RGBA, cov float64) {
	if !image.Pt(x, y).In(img.Rect) {
		return
	}
	o := img.RGBAAt(x, y)
	mix := func(dst, src uint8) uint8 { return uint8(math.Round(float64(dst)*(1-cov) + float64(src)*cov)) }
	img.SetRGBA(x, y, color.RGBA{R: mix(o.R, c.R), G: mix(o.G, c.G), B: mix(o.B, c.B), A: 255})
}
