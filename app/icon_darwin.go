package main

import _ "embed"

// icon is a template image: black on transparent, which macOS tints to match the menu bar (icon/gen.go draws it).
//
//go:embed icon.png
var icon []byte

const iconIsTemplate = true
