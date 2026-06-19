package render

import (
	"image"
	"image/color"
	"testing"
)

// allWhite builds a blank (all palette index 0 = white) 250x122 image.
func allWhite() *image.Paletted {
	pal := color.Palette{color.White, color.Black}
	return image.NewPaletted(image.Rect(0, 0, Width, Height), pal)
}

// TestGiciskyEncodeShape verifies the model 0x00A0 raw format: a single BW
// plane, 125*264 bits = 4125 bytes, no headers/prefix.
func TestGiciskyEncodeShape(t *testing.T) {
	w, h := encodedDims(DefaultOpts())
	wantTotal := w * h / 8 // 264*125/8 = 4125
	b := GiciskyEncode(Image(Item{Name: "House Drip Coffee", PriceCents: 295}))
	if len(b) != wantTotal {
		t.Fatalf("encoded length = %d, want %d", len(b), wantTotal)
	}
}

// TestGiciskyEncodeWhiteBits verifies an all-white image encodes as all 0xFF
// (bit=1 means white).
func TestGiciskyEncodeWhiteBits(t *testing.T) {
	b := GiciskyEncode(allWhite())
	for i, by := range b {
		if by != 0xFF {
			t.Fatalf("white image byte %d = 0x%02x, want 0xFF", i, by)
		}
	}
}
