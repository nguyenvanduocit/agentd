package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
)

func decode(t *testing.T, name string) image.Image {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if s := img.Bounds().Size(); s.X != 32 || s.Y != 32 {
		t.Fatalf("%s is %v, want 32×32 (16 points at 2×)", name, s)
	}
	return img
}

// macOS tints a template image by its alpha alone: anything but black on transparent shows as a smudge.
func TestTheMacIconIsATemplate(t *testing.T) {
	img := decode(t, "icon.png")
	inked := 0
	for y := range 32 {
		for x := range 32 {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA) //nolint:forcetypeassert // NRGBAModel converts to NRGBA
			switch {
			case c.A == 0:
			case c == color.NRGBA{A: 255}:
				inked++
			default:
				t.Fatalf("pixel %d,%d is %v: a template is black on transparent", x, y, c)
			}
		}
	}
	if inked == 0 {
		t.Fatal("icon.png is empty")
	}
}

// Windows and Linux draw the icon as it is: the badge is opaque where it is drawn, so it shows on any taskbar.
func TestTheBadgeIsOpaqueWithTheGlyphInAnotherColor(t *testing.T) {
	img := decode(t, "icon-badge.png")
	colors := map[color.NRGBA]int{}
	for y := range 32 {
		for x := range 32 {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA) //nolint:forcetypeassert // NRGBAModel converts to NRGBA
			if c.A != 0 && c.A != 255 {
				t.Fatalf("pixel %d,%d is %v: half-transparent", x, y, c)
			}
			if c.A == 255 {
				colors[c]++
			}
		}
	}
	if len(colors) != 2 {
		t.Fatalf("badge colors = %v, want the badge and the glyph", colors)
	}
}
