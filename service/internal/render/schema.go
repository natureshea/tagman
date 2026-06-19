package render

import (
	"image"
	"image/color"
)

// Schema is a named, declarative tag layout mapping an item's Name + Price into
// regions on the source-space face, interpreted by RenderSchema. Regions are in
// source pixels (0,0 = top-left of the 250x122 face); the encode pipeline (2:1
// reduction + rotate + calibrated fit) is applied afterwards for every schema.
type Schema struct {
	Name  string     // preset id, e.g. "price-tag"
	Title TextBox    // item name
	Price PriceBox   // formatted price
	Label *StaticBox // optional fixed text (store name, "EA", …); nil = none
}

// Align positions text horizontally within its region's width.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// TextBox lays out wrapped vector text within a region. When the text needs
// more than MaxLines, the last line is ellipsised. SizePx is the font size in
// source-space pixels.
type TextBox struct {
	X, Y, W, H int
	SizePx     float64
	Font       FontID
	MaxLines   int
	Align      Align
}

// PriceBox lays out the formatted price on a single line within a region.
type PriceBox struct {
	X, Y, W, H int
	SizePx     float64
	Font       FontID
	Align      Align
}

// StaticBox is fixed text baked into the schema (not from the catalog).
type StaticBox struct {
	Text   string
	X, Y   int
	W      int
	SizePx float64
	Font   FontID
	Align  Align
}

// DefaultSchema is the calibrated v1 price tag: name wrapped at the top (2 lines
// then ellipsis), price large at the bottom-left. Vector font (Go Bold).
func DefaultSchema() Schema {
	return Schema{
		Name:  "price-tag",
		Title: TextBox{X: 6, Y: 6, W: Width - 12, H: 64, SizePx: 28, Font: FontBold, MaxLines: 2, Align: AlignLeft},
		Price: PriceBox{X: 6, Y: Height - 40, W: Width - 12, H: 40, SizePx: 32, Font: FontBold, Align: AlignLeft},
	}
}

// RenderSchema converts an item into a 1bpp face using the schema.
func RenderSchema(s Schema, it Item) *image.Paletted {
	pal := color.Palette{color.White, color.Black}
	img := image.NewPaletted(image.Rect(0, 0, Width, Height), pal) // white bg

	if s.Label != nil {
		b := *s.Label
		drawAligned(img, b.Text, b.X, b.Y, b.W, b.Font, b.SizePx, b.Align)
	}

	// Item name: wrapped, ellipsised, aligned.
	lines := wrapTTF(it.Name, s.Title.Font, s.Title.SizePx, s.Title.W, maxLines(s.Title.MaxLines))
	ly := s.Title.Y
	lineH := ttfLineHeight(s.Title.Font, s.Title.SizePx)
	for _, ln := range lines {
		drawAligned(img, ln, s.Title.X, ly, s.Title.W, s.Title.Font, s.Title.SizePx, s.Title.Align)
		ly += lineH
	}

	// Price: single line, aligned.
	price := formatPrice(it.PriceCents)
	drawAligned(img, price, s.Price.X, s.Price.Y, s.Price.W, s.Price.Font, s.Price.SizePx, s.Price.Align)

	return img
}

func maxLines(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// drawAligned draws one line of vector text, horizontally aligned within w.
func drawAligned(img *image.Paletted, s string, x, y, w int, id FontID, px float64, a Align) {
	lw := ttfWidth(s, id, px)
	switch a {
	case AlignCenter:
		x += (w - lw) / 2
	case AlignRight:
		x += w - lw
	}
	drawTTFLine(img, s, x, y, id, px)
}

// wrapTTF greedily wraps s to fit maxW at the given font/size, up to maxLines.
// If text remains, the last line is rune-trimmed and an ellipsis appended.
func wrapTTF(s string, id FontID, px float64, maxW, maxLines int) []string {
	words := splitWords(s)
	var lines []string
	line := ""
	i := 0
	for ; i < len(words); i++ {
		try := words[i]
		if line != "" {
			try = line + " " + words[i]
		}
		if ttfWidth(try, id, px) > maxW && line != "" {
			lines = append(lines, line)
			line = words[i]
			if len(lines) == maxLines {
				break
			}
		} else {
			line = try
		}
	}

	truncated := false
	if len(lines) < maxLines {
		if line != "" {
			lines = append(lines, line)
		}
	} else if line != "" || i < len(words)-1 {
		truncated = true // hit the line cap with content still unplaced
	}

	if truncated && len(lines) > 0 {
		r := []rune(lines[len(lines)-1])
		for len(r) > 0 && ttfWidth(string(r)+"…", id, px) > maxW {
			r = r[:len(r)-1]
		}
		lines[len(lines)-1] = string(r) + "…"
	}
	return lines
}
