package render

import (
	"image"
	"image/color"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/gofont/gosmallcaps"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Vector-font rendering: draw a TrueType face at the requested pixel size, then
// threshold to 1bpp. The Go fonts ship in golang.org/x/image; several weights
// are available without external files.

// FontID selects a bundled Go font face. Zero value = FontBold.
type FontID int

const (
	FontBold      FontID = iota // Go Bold, best default for small 1bpp
	FontRegular                 // Go Regular
	FontMedium                  // Go Medium
	FontMono                    // Go Mono
	FontMonoBold                // Go Mono Bold
	FontSmallCaps               // Go Smallcaps
)

// FontByName maps a name to a FontID; unknown falls back to FontBold. Names:
// bold, regular, medium, mono, monobold, smallcaps.
func FontByName(s string) FontID {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "regular", "go":
		return FontRegular
	case "medium":
		return FontMedium
	case "mono":
		return FontMono
	case "monobold":
		return FontMonoBold
	case "smallcaps":
		return FontSmallCaps
	default:
		return FontBold
	}
}

var fontTTF = map[FontID][]byte{
	FontBold:      gobold.TTF,
	FontRegular:   goregular.TTF,
	FontMedium:    gomedium.TTF,
	FontMono:      gomono.TTF,
	FontMonoBold:  gomonobold.TTF,
	FontSmallCaps: gosmallcaps.TTF,
}

type faceKey struct {
	id  FontID
	px10 int
}

var (
	fontMu      sync.Mutex
	parsedFonts = map[FontID]*opentype.Font{}
	faceCache   = map[faceKey]font.Face{}
)

func parsedFont(id FontID) *opentype.Font {
	if f, ok := parsedFonts[id]; ok {
		return f
	}
	ttf, ok := fontTTF[id]
	if !ok {
		ttf = fontTTF[FontBold]
	}
	f, err := opentype.Parse(ttf)
	if err != nil {
		panic("render: parse font: " + err.Error())
	}
	parsedFonts[id] = f
	return f
}

// faceFor returns a cached face for (font, pixel size). At DPI 72, points == px.
func faceFor(id FontID, px float64) font.Face {
	key := faceKey{id: id, px10: int(px*10 + 0.5)}
	fontMu.Lock()
	defer fontMu.Unlock()
	if fc, ok := faceCache[key]; ok {
		return fc
	}
	fc, err := opentype.NewFace(parsedFont(id), &opentype.FaceOptions{
		Size:    px,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		fc = nil
	}
	faceCache[key] = fc
	return fc
}

// ttfWidth is the rendered width (px) of s in the given font/size.
func ttfWidth(s string, id FontID, px float64) int {
	fc := faceFor(id, px)
	if fc == nil {
		return 0
	}
	return font.MeasureString(fc, s).Ceil()
}

// ttfLineHeight is the line advance (px) for the given font/size.
func ttfLineHeight(id FontID, px float64) int {
	fc := faceFor(id, px)
	if fc == nil {
		return int(px)
	}
	m := fc.Metrics()
	return m.Ascent.Ceil() + m.Descent.Ceil()
}

// drawTTFLine draws one line with top-left at (x, yTop), thresholding AA glyphs
// to black ink (palette index 1). SetColorIndex clips OOB.
func drawTTFLine(dst *image.Paletted, s string, x, yTop int, id FontID, px float64) {
	fc := faceFor(id, px)
	if fc == nil || s == "" {
		return
	}
	m := fc.Metrics()
	asc := m.Ascent.Ceil()
	h := asc + m.Descent.Ceil()
	w := font.MeasureString(fc, s).Ceil()
	if w < 1 || h < 1 {
		return
	}
	tmp := image.NewGray(image.Rect(0, 0, w, h))
	d := &font.Drawer{
		Dst:  tmp,
		Src:  image.NewUniform(color.Gray{Y: 255}),
		Face: fc,
		Dot:  fixed.P(0, asc),
	}
	d.DrawString(s)
	for ty := 0; ty < h; ty++ {
		for tx := 0; tx < w; tx++ {
			if tmp.GrayAt(tx, ty).Y > 127 { // threshold AA -> ink
				dst.SetColorIndex(x+tx, yTop+ty, 1)
			}
		}
	}
}
