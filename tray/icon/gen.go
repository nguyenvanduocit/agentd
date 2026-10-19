//go:build ignore

// Writes the tray icons from one 16×16 pixel glyph, a prompt (>_) in a frame, drawn at 2× (32×32 for high-resolution
// menu bars). Run: go run icon/gen.go
//
//	../icon.png        black on transparent: macOS tints a template image to match the menu bar
//	../icon-badge.png  the prompt in white on a filled badge: Windows and Linux draw an icon as it is, and a black
//	                   glyph would vanish on a dark taskbar, a white one on a light one
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

const scale = 2

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

func draw(glyph []string, colors map[rune]color.NRGBA) *image.NRGBA {
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

func write(name string, img image.Image) {
	f, err := os.Create(name)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func main() {
	write("icon.png", draw(frame, map[rune]color.NRGBA{'#': black}))
	write("icon-badge.png", draw(badge, map[rune]color.NRGBA{'#': indigo, 'o': white}))
}
