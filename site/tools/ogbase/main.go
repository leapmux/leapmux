// Command ogbase generates the static background for the docs site's Open
// Graph cards: a 1200x630 PNG with a diagonal teal-to-charcoal gradient and a
// thin teal accent rule along the bottom edge. The per-page card text (wordmark,
// section eyebrow, title, URL) is overlaid at build time by Hugo's images.Text
// filter -- see site/layouts/_partials/og-image.html.
//
// This is a one-off asset generator, not part of the normal site build. Re-run
// it only when the card background design changes:
//
//	cd site && GOWORK=off go run ./tools/ogbase assets/og/base.png
//
// Colors track the brand palette in site/assets/css/variables.css (teal accent
// #14b8a6, warm charcoal background #1a1917).
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

const (
	width  = 1200
	height = 630
)

// lerp linearly interpolates between two 8-bit channel values.
func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t)
}

func main() {
	out := "assets/og/base.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	// Diagonal gradient: deep teal-charcoal at the top-left fading to
	// near-black at the bottom-right, so the title area (left) reads against
	// the brand-tinted side.
	topLeft := color.RGBA{R: 0x15, G: 0x30, B: 0x2a, A: 0xff}
	bottomRight := color.RGBA{R: 0x12, G: 0x12, B: 0x11, A: 0xff}
	accent := color.RGBA{R: 0x14, G: 0xb8, B: 0xa6, A: 0xff}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Normalize the diagonal position to [0,1].
			t := (float64(x)/float64(width) + float64(y)/float64(height)) / 2
			img.Set(x, y, color.RGBA{
				R: lerp(topLeft.R, bottomRight.R, t),
				G: lerp(topLeft.G, bottomRight.G, t),
				B: lerp(topLeft.B, bottomRight.B, t),
				A: 0xff,
			})
		}
	}

	// Thin teal accent rule along the bottom edge (6px).
	for y := height - 6; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, accent)
		}
	}

	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
