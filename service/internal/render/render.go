// Package render turns a catalog item into a 1bpp framebuffer for the 2.13in
// 250x122 BW Gicisky panel, plus a PNG preview.
package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	Width  = 250
	Height = 122
)

// Item is the renderer's input. Separate from items.Item to break the import
// cycle.
type Item struct {
	Name       string
	PriceCents int64
}

// Image draws the tag face with the default schema. Black pixel = ink.
func Image(it Item) *image.Paletted {
	return RenderSchema(DefaultSchema(), it)
}

// Pack packs the image to 1bpp. MSB-first, row-major: bit (7-x%8) of byte
// y*stride+x/8 is pixel (x,y).
func Pack(img *image.Paletted) []byte {
	stride := (Width + 7) / 8
	buf := make([]byte, stride*Height)
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			if img.ColorIndexAt(x, y) == 1 { // black
				buf[y*stride+x/8] |= 1 << (7 - uint(x%8))
			}
		}
	}
	return buf
}

// Encode geometry for eigger model 0x00A0 (TFT 2.1" BW).
const (
	encSrcH = 132 // panel logical height (122 render + pad)
	encThr  = 128 // lum > this = white
)

// EncodeOpts controls the Gicisky encode. Zero value is not the default; use
// DefaultOpts().
//
// From eigger writer.py _make_image_packet: build a panel image, optionally
// tft-reshape to (w/2, h*2), rotate CCW expand, then pack MSB-first with
// 1 = white (lum > threshold). MirrorX/MirrorY reverse the walk. Single BW
// plane, no red, no 0x75 framing, no prefix.
type EncodeOpts struct {
	TFT      bool // tft reshape: resize to (w/2, h*2) before rotate
	Rotation int  // CCW expand rotation in degrees: 0, 90, 180, 270
	MirrorX  bool // walk each row right-to-left
	MirrorY  bool // walk rows bottom-to-top
	Invert   bool    // 1 = black instead of 1 = white
	SrcW     int     // panel-logical source width  before reshape (0 = Width)
	SrcH     int     // panel-logical source height before reshape (0 = encSrcH)
	Scale    float64 // content scale, centered in the source canvas (0 = 1.0)
	OffX     int     // extra x offset (source px) added after centering
	OffY     int     // extra y offset (source px) added after centering
}

// DefaultOpts is eigger model 0x00A0 plus the calibrated fit: tft, rotate 90,
// mirror_x, ox=-1/oy=5 to center the image in the visible window.
func DefaultOpts() EncodeOpts {
	return EncodeOpts{TFT: true, Rotation: 90, MirrorX: true, OffX: -1, OffY: 5}
}

// srcDims resolves the source dimensions, filling defaults.
func srcDims(o EncodeOpts) (int, int) {
	w, h := o.SrcW, o.SrcH
	if w <= 0 {
		w = Width
	}
	if h <= 0 {
		h = encSrcH
	}
	return w, h
}

// GiciskyEncode encodes with default options.
func GiciskyEncode(img *image.Paletted) []byte {
	return GiciskyEncodeOpts(img, DefaultOpts())
}

// grayPanel builds a sw x sh grayscale canvas, white bg. The face is scaled by
// scale (<=0 means 1.0) and centered.
func grayPanel(img *image.Paletted, sw, sh int, scale float64, offX, offY int) *image.Gray {
	src := image.NewGray(image.Rect(0, 0, sw, sh))
	for i := range src.Pix {
		src.Pix[i] = 0xFF
	}
	if scale <= 0 {
		scale = 1.0
	}
	cw := int(float64(Width) * scale)
	ch := int(float64(Height) * scale)
	if cw < 1 || ch < 1 {
		return src
	}
	ox := (sw-cw)/2 + offX // center, then nudge
	oy := (sh-ch)/2 + offY
	for dy := 0; dy < ch; dy++ {
		ty := oy + dy
		if ty < 0 || ty >= sh {
			continue
		}
		sy := dy * Height / ch // nearest-neighbour source row
		for dx := 0; dx < cw; dx++ {
			tx := ox + dx
			if tx < 0 || tx >= sw {
				continue
			}
			sx := dx * Width / cw
			if img.ColorIndexAt(sx, sy) == 1 {
				src.SetGray(tx, ty, color.Gray{Y: 0})
			}
		}
	}
	return src
}

// rotateCCW rotates a Gray image CCW by 90*k degrees, expanding. Matches PIL
// rotate(90*k, expand=True).
func rotateCCW(src *image.Gray, k int) *image.Gray {
	k = ((k % 4) + 4) % 4
	g := src
	for i := 0; i < k; i++ {
		ow := g.Bounds().Dx()
		oh := g.Bounds().Dy()
		n := image.NewGray(image.Rect(0, 0, oh, ow))
		for ny := 0; ny < ow; ny++ {
			for nx := 0; nx < oh; nx++ {
				n.SetGray(nx, ny, g.GrayAt(ow-1-ny, nx))
			}
		}
		g = n
	}
	return g
}

// GiciskyEncodeOpts encodes the face to the panel's raw buffer.
func GiciskyEncodeOpts(img *image.Paletted, o EncodeOpts) []byte {
	sw, sh := srcDims(o)
	g := grayPanel(img, sw, sh, o.Scale, o.OffX, o.OffY)

	// Reshape to (w/2, h*2) for the panel's 2:1 pixels. A dest pixel is black if
	// either source column is black, so thin strokes survive.
	if o.TFT {
		w := g.Bounds().Dx() / 2
		h := g.Bounds().Dy() * 2
		rz := image.NewGray(image.Rect(0, 0, w, h))
		for i := range rz.Pix {
			rz.Pix[i] = 0xFF
		}
		for dy := 0; dy < h; dy++ {
			sy := dy / 2
			for dx := 0; dx < w; dx++ {
				black := g.GrayAt(2*dx, sy).Y < encThr || g.GrayAt(2*dx+1, sy).Y < encThr
				if black {
					rz.SetGray(dx, dy, color.Gray{Y: 0})
				}
			}
		}
		g = rz
	}

	g = rotateCCW(g, o.Rotation/90)

	w := g.Bounds().Dx()
	h := g.Bounds().Dy()
	out := make([]byte, 0, (w*h+7)/8)
	var b byte
	nbits := 0
	emit := func(white bool) {
		b <<= 1
		if white {
			b |= 1
		}
		if nbits++; nbits == 8 {
			out = append(out, b)
			b, nbits = 0, 0
		}
	}
	for yi := 0; yi < h; yi++ {
		y := yi
		if o.MirrorY {
			y = h - 1 - yi
		}
		for xi := 0; xi < w; xi++ {
			x := xi
			if o.MirrorX {
				x = w - 1 - xi
			}
			white := g.GrayAt(x, y).Y > encThr
			if o.Invert {
				white = !white
			}
			emit(white)
		}
	}
	if nbits > 0 {
		out = append(out, b<<uint(8-nbits))
	}
	return out
}

// encodedDims returns the rotated framebuffer dimensions.
func encodedDims(o EncodeOpts) (int, int) {
	w, h := srcDims(o)
	if o.TFT {
		w, h = w/2, h*2
	}
	if (o.Rotation/90)%2 != 0 {
		w, h = h, w
	}
	return w, h
}

// NativeDims is the panel's true framebuffer grid. Authoring here skips
// resampling, keeping 1px strokes 1px.
const (
	NativeW = encSrcH * 2 // 264
	NativeH = Width / 2   // 125
)

// RotatePalettedCCW rotates a paletted image CCW by 90*k degrees. Pure index
// remap, lossless. Cancels the panel's scan rotation in the native path.
func RotatePalettedCCW(src *image.Paletted, k int) *image.Paletted {
	k = ((k % 4) + 4) % 4
	g := src
	for i := 0; i < k; i++ {
		ow, oh := g.Bounds().Dx(), g.Bounds().Dy()
		n := image.NewPaletted(image.Rect(0, 0, oh, ow), g.Palette)
		for ny := 0; ny < ow; ny++ {
			for nx := 0; nx < oh; nx++ {
				n.SetColorIndex(nx, ny, g.ColorIndexAt(ow-1-ny, nx))
			}
		}
		g = n
	}
	return g
}

// PackNative packs an image already at the panel grid and orientation.
// MSB-first, row-major, 1 = white (palette index 0), mirror-aware.
func PackNative(img *image.Paletted, o EncodeOpts) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]byte, 0, (w*h+7)/8)
	var b byte
	nbits := 0
	emit := func(white bool) {
		b <<= 1
		if white {
			b |= 1
		}
		if nbits++; nbits == 8 {
			out = append(out, b)
			b, nbits = 0, 0
		}
	}
	for yi := 0; yi < h; yi++ {
		y := yi
		if o.MirrorY {
			y = h - 1 - yi
		}
		for xi := 0; xi < w; xi++ {
			x := xi
			if o.MirrorX {
				x = w - 1 - xi
			}
			white := img.ColorIndexAt(x, y) == 0 // index 0 = white
			if o.Invert {
				white = !white
			}
			emit(white)
		}
	}
	if nbits > 0 {
		out = append(out, b<<uint(8-nbits))
	}
	return out
}

// fillRectB fills a black rect, clipped to bounds.
func fillRectB(img *image.Paletted, x0, y0, x1, y1 int) {
	bnd := img.Bounds()
	for y := y0; y < y1; y++ {
		if y < bnd.Min.Y || y >= bnd.Max.Y {
			continue
		}
		for x := x0; x < x1; x++ {
			if x < bnd.Min.X || x >= bnd.Max.X {
				continue
			}
			img.SetColorIndex(x, y, 1)
		}
	}
}

// drawTextScaled draws basicfont text scaled by sc. Each lit pixel becomes an
// (sc+1)^2 block; the 1px overlap fakes bold and survives the 2:1 reduction.
// (x,y) is top-left.
func drawTextScaled(img *image.Paletted, s string, x, y, sc int) {
	face := basicfont.Face7x13
	tw := textWidth(face, s)
	if tw < 1 {
		return
	}
	const th = 13
	pal := color.Palette{color.White, color.Black}
	tmp := image.NewPaletted(image.Rect(0, 0, tw, th), pal)
	drawText(tmp, s, 0, th-3, 1) // baseline near bottom of the 13px cell
	for ty := 0; ty < th; ty++ {
		for tx := 0; tx < tw; tx++ {
			if tmp.ColorIndexAt(tx, ty) == 1 {
				fillRectB(img, x+tx*sc, y+ty*sc, x+tx*sc+sc+1, y+ty*sc+sc+1)
			}
		}
	}
}

// TestPatternNative draws an orientation/scale diagnostic at the native grid.
// Asymmetric to make rotation and mirror obvious: 3px border, corner blobs (TL
// small to BR largest), a block F, and TOP near the top edge.
func TestPatternNative(w, h int) *image.Paletted {
	pal := color.Palette{color.White, color.Black}
	img := image.NewPaletted(image.Rect(0, 0, w, h), pal)

	// 3px border on all four sides.
	fillRectB(img, 0, 0, w, 3)
	fillRectB(img, 0, h-3, w, h)
	fillRectB(img, 0, 0, 3, h)
	fillRectB(img, w-3, 0, w, h)

	// Corner blobs, distinct fixed sizes.
	fillRectB(img, 5, 5, 13, 13)         // TL 8
	fillRectB(img, w-19, 5, w-5, 19)     // TR 14
	fillRectB(img, 5, h-25, 25, h-5)     // BL 20
	fillRectB(img, w-31, h-31, w-5, h-5) // BR 26

	// 40x40 square: reads as a rectangle if pixel aspect is off.
	fillRectB(img, 8, 8, 48, 48)
	fillRectWhite(img, 14, 14, 42, 42)

	// Block F: stem 8px, 60px tall, 36px arms.
	fx, fy := 8, 56
	fillRectB(img, fx, fy, fx+8, fy+60)     // vertical stem
	fillRectB(img, fx, fy, fx+36, fy+8)     // top arm
	fillRectB(img, fx, fy+26, fx+28, fy+34) // middle arm

	drawTextScaled(img, "TOP", 8, 130, 3)
	return img
}

// fillRectWhite clears a rect to white, clipped to bounds.
func fillRectWhite(img *image.Paletted, x0, y0, x1, y1 int) {
	bnd := img.Bounds()
	for y := y0; y < y1; y++ {
		if y < bnd.Min.Y || y >= bnd.Max.Y {
			continue
		}
		for x := x0; x < x1; x++ {
			if x < bnd.Min.X || x >= bnd.Max.X {
				continue
			}
			img.SetColorIndex(x, y, 0)
		}
	}
}

// Preview returns a PNG of the rendered face.
func Preview(it Item) ([]byte, error) {
	return PreviewImage(Image(it))
}

// PreviewImage renders a paletted face to a 3x PNG.
func PreviewImage(src *image.Paletted) ([]byte, error) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	scale := 3
	out := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))
	for y := 0; y < h*scale; y++ {
		for x := 0; x < w*scale; x++ {
			var c color.Color = color.White
			if src.ColorIndexAt(x/scale, y/scale) == 1 {
				c = color.Black
			}
			out.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// EncodedPreview decodes the transmitted framebuffer (default opts) back to a
// viewable PNG, to check orientation against Preview().
func EncodedPreview(it Item) ([]byte, error) {
	return EncodedPreviewOpts(Image(it), DefaultOpts())
}

// EncodedPreviewOpts decodes the encoded bitstream back to a 3x PNG using the
// encoder's walk order. The preview matches the wire bytes.
func EncodedPreviewOpts(img *image.Paletted, o EncodeOpts) ([]byte, error) {
	enc := GiciskyEncodeOpts(img, o)
	fbW, fbH := encodedDims(o)
	const scale = 3
	out := image.NewRGBA(image.Rect(0, 0, fbW*scale, fbH*scale))
	i := 0
	put := func(x, y int, white bool) {
		c := color.Color(color.Black)
		if white {
			c = color.White
		}
		for sy := 0; sy < scale; sy++ {
			for sx := 0; sx < scale; sx++ {
				out.Set(x*scale+sx, y*scale+sy, c)
			}
		}
	}
	// Same walk as the encoder: yi outer, xi inner, mirror-aware.
	for yi := 0; yi < fbH; yi++ {
		y := yi
		if o.MirrorY {
			y = fbH - 1 - yi
		}
		for xi := 0; xi < fbW; xi++ {
			x := xi
			if o.MirrorX {
				x = fbW - 1 - xi
			}
			byteIdx, bit := i/8, 7-(i%8)
			white := byteIdx < len(enc) && (enc[byteIdx]>>uint(bit))&1 == 1
			put(x, y, white)
			i++
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// fillRect fills a black rect [x0,x1) x [y0,y1), clipped.
func fillRect(img *image.Paletted, x0, y0, x1, y1 int) {
	for y := y0; y < y1; y++ {
		if y < 0 || y >= Height {
			continue
		}
		for x := x0; x < x1; x++ {
			if x < 0 || x >= Width {
				continue
			}
			img.SetColorIndex(x, y, 1)
		}
	}
}

// TestPatternImage builds an asymmetric diagnostic face for reading panel
// orientation, mirroring, and clipping off a tag: 1px border, corner blobs
// (TL=6 TR=12 BL=18 BR=24), a block F, and TOP.
func TestPatternImage() *image.Paletted {
	pal := color.Palette{color.White, color.Black}
	img := image.NewPaletted(image.Rect(0, 0, Width, Height), pal)

	// 1px border.
	fillRect(img, 0, 0, Width, 1)
	fillRect(img, 0, Height-1, Width, Height)
	fillRect(img, 0, 0, 1, Height)
	fillRect(img, Width-1, 0, Width, Height)

	// Corner blobs: TL smallest, BR largest.
	fillRect(img, 2, 2, 8, 8)                             // TL 6
	fillRect(img, Width-14, 2, Width-2, 14)               // TR 12
	fillRect(img, 2, Height-20, 20, Height-2)             // BL 18
	fillRect(img, Width-26, Height-26, Width-2, Height-2) // BR 24

	// Block F, center-left.
	fx, fy := 40, 24
	fillRect(img, fx, fy, fx+14, fy+74)    // vertical stem
	fillRect(img, fx, fy, fx+70, fy+14)    // top arm
	fillRect(img, fx, fy+32, fx+52, fy+44) // middle arm

	drawTextScaled(img, "TOP", 120, 26, 4)
	return img
}

func formatPrice(cents int64) string {
	d := cents / 100
	c := cents % 100
	if c < 0 {
		c = -c
	}
	return "$" + itoa(d) + "." + pad2(c)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(buf[i:])
	if neg {
		s = "-" + s
	}
	return s
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// --- text drawing (basicfont, 7x13) ---

func drawText(img *image.Paletted, s string, x, y int, idx uint8) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(img.Palette[idx]),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func textWidth(face font.Face, s string) int {
	d := &font.Drawer{Face: face}
	return d.MeasureString(s).Round()
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
