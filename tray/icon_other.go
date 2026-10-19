//go:build !darwin

package main

import _ "embed"

// icon is drawn as it is on Windows and Linux, which have no template images: a filled badge that shows on a light
// taskbar and on a dark one (icon/gen.go draws it).
//
//go:embed icon-badge.png
var icon []byte

const iconIsTemplate = false
