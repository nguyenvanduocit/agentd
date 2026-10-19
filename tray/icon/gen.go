//go:build ignore

// Writes agentd's icons from one 16×16 pixel glyph, a prompt (>_) in a frame. Run from tray/: go run icon/gen.go
//
//	icon.png                  the tray on macOS, at 2× (32×32): black on transparent, a template macOS tints to
//	                          match the menu bar
//	icon-badge.png            the tray on Windows and Linux, at 2×: the prompt in white on a filled badge, because they
//	                          draw an icon as it is, and a black glyph would vanish on a dark taskbar, a white one on a
//	                          light one
//	../daemon/app/agentd.icns the badge as the icon of agentd.app (macOS), at every size iconutil makes
//	../daemon/app/agentd.png  the badge at 256×256, the icon of agentd's desktop entry (Linux)
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
)

// appImages are the images of an .icns file as iconutil writes them: the entry type and its side in pixels.
var appImages = []struct {
	kind string
	px   int
}{
	{"icp4", 16}, {"icp5", 32}, {"ic11", 32}, {"ic12", 64}, {"ic07", 128}, {"ic13", 256}, {"ic08", 256}, {"ic14", 512}, {"ic09", 512}, {"ic10", 1024},
}

// frame is the template: the frame's outline and the prompt inside it.
var frame = []string{
	"................",
	"..############..",
	".#............#.",
	".#............#.",
	".#.##.........#.",
	".#..##........#.",
	".#...##.......#.",
	".#....##......#.",
	".#...##.......#.",
	".#..##........#.",
	".#.##...####..#.",
	".#......####..#.",
	".#............#.",
	".#............#.",
	"..############..",
	"................",
}

// badge is the frame filled in: '#' is the badge, 'o' the prompt cut out of it.
var badge = []string{
	"................",
	"..############..",
	".##############.",
	".##############.",
	".##oo##########.",
	".###oo#########.",
	".####oo########.",
	".#####oo#######.",
	".####oo########.",
	".###oo#########.",
	".##oo###oooo###.",
	".#######oooo###.",
	".##############.",
	".##############.",
	"..############..",
	"................",
}

var (
	black  = color.NRGBA{A: 255}
	indigo = color.NRGBA{R: 0x4f, G: 0x46, B: 0xe5, A: 255}
	white  = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
)

func draw(glyph []string, colors map[rune]color.NRGBA, scale int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 16*scale, 16*scale))
	for y, row := range glyph {
		for x, c := range row {
			col, ok := colors[c]
			if !ok {
				continue
			}
			for dy := range scale {
				for dx := range scale {
					img.Set(x*scale+dx, y*scale+dy, col)
				}
			}
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// icns is an .icns file: "icns" and its length, then each image as its type, its length and its PNG.
func icns(colors map[rune]color.NRGBA) []byte {
	var body bytes.Buffer
	for _, im := range appImages {
		data := encode(draw(badge, colors, im.px/16))
		body.WriteString(im.kind)
		_ = binary.Write(&body, binary.BigEndian, uint32(8+len(data)))
		body.Write(data)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func write(name string, data []byte) {
	if err := os.WriteFile(name, data, 0o644); err != nil {
		panic(err)
	}
}

func main() {
	onBadge := map[rune]color.NRGBA{'#': indigo, 'o': white}
	write("icon.png", encode(draw(frame, map[rune]color.NRGBA{'#': black}, 2)))
	write("icon-badge.png", encode(draw(badge, onBadge, 2)))
	if err := os.MkdirAll("../daemon/app", 0o755); err != nil {
		panic(err)
	}
	write("../daemon/app/agentd.icns", icns(onBadge))
	write("../daemon/app/agentd.png", encode(draw(badge, onBadge, 16)))
}
