package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"maps"
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

// agentd.app's icon is an .icns file of PNGs, one per size macOS asks for; the desktop entry's is one PNG.
func TestTheAppIconsHoldEverySize(t *testing.T) {
	b, err := os.ReadFile("../daemon/app/agentd.icns")
	if err != nil {
		t.Fatal(err)
	}
	if string(b[:4]) != "icns" || int(binary.BigEndian.Uint32(b[4:8])) != len(b) {
		t.Fatalf("agentd.icns header %q %d, file %d bytes", b[:4], binary.BigEndian.Uint32(b[4:8]), len(b))
	}
	sides := map[string]int{}
	for at := 8; at < len(b); {
		kind, size := string(b[at:at+4]), int(binary.BigEndian.Uint32(b[at+4:at+8]))
		img, err := png.Decode(bytes.NewReader(b[at+8 : at+size]))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		sides[kind] = img.Bounds().Dx()
		at += size
	}
	want := map[string]int{"icp4": 16, "icp5": 32, "ic11": 32, "ic12": 64, "ic07": 128, "ic13": 256, "ic08": 256, "ic14": 512, "ic09": 512, "ic10": 1024}
	if !maps.Equal(sides, want) {
		t.Fatalf("icns images %v, want %v", sides, want)
	}
	if side := decodeAny(t, "../daemon/app/agentd.png").Bounds().Dx(); side != 256 {
		t.Fatalf("agentd.png is %d px wide, want 256", side)
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
