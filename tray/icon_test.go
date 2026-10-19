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

// agentd.app's icon, from which mygo build makes the .icns: a square of 1024, the badge opaque and its corners clear.
func TestTheAppIconIsTheBadgeAt1024(t *testing.T) {
	img := decodeAny(t, "icon/app.png")
	if s := img.Bounds().Size(); s.X != 1024 || s.Y != 1024 {
		t.Fatalf("icon/app.png is %v, want 1024×1024", s)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the corner is not clear")
	}
	if _, _, _, a := img.At(512, 120).RGBA(); a != 0xffff {
		t.Fatal("the badge is not opaque")
	}
}

func decodeAny(t *testing.T, name string) image.Image {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}
