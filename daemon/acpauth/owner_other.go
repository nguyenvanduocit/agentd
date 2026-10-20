//go:build !unix

package acpauth

import "io/fs"

// ownedBy: Windows keeps its files in the user's profile, which other users cannot enter; there is no uid to compare.
func ownedBy(fs.FileInfo, int) bool { return true }

// currentUID: there is no uid on Windows.
func currentUID() int { return -1 }
