//go:build ignore

// Writes ../icon.png: the tray icon, an 8×8 pixel glyph drawn at 4× (32×32, for high-resolution menu bars), black on
// transparent so that macOS tints it as a template. Run: go run icon/gen.go
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

var glyph = []string{
	"########",
	"#......#",
	"#.####.#",
	"#.#..#.#",
	"#.#..#.#",
	"#.####.#",
	"#......#",
	"########",
}

func main() {
	const scale = 4
	img := image.NewNRGBA(image.Rect(0, 0, 8*scale, 8*scale))
	for y, row := range glyph {
		for x, c := range row {
			if c != '#' {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(x*scale+dx, y*scale+dy, color.NRGBA{A: 255})
				}
			}
		}
	}
	f, err := os.Create("icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
