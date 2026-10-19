//go:build ignore

// Writes agentd's icons from one 16×16 pixel glyph, a prompt (>_) in a frame. Run from tray/: go run icon/gen.go
//
//	icon.png                  the tray on macOS, at 2× (32×32): black on transparent, a template macOS tints to
//	                          match the menu bar
//	icon-badge.png            the tray on Windows and Linux, at 2×: the prompt in white on a filled badge, because they
//	                          draw an icon as it is, and a black glyph would vanish on a dark taskbar, a white one on a
//	                          light one
//	icon/app.png              the badge at 1024×1024, the icon of agentd.app (mygo.json "icon"; mygo build makes the
//	                          .icns of macOS and the icons of other systems from it)
package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
)

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

func write(name string, data []byte) {
	if err := os.WriteFile(name, data, 0o644); err != nil {
		panic(err)
	}
}

func main() {
	onBadge := map[rune]color.NRGBA{'#': indigo, 'o': white}
	write("icon.png", encode(draw(frame, map[rune]color.NRGBA{'#': black}, 2)))
	write("icon-badge.png", encode(draw(badge, onBadge, 2)))
	write("icon/app.png", encode(draw(badge, onBadge, 64)))
}
