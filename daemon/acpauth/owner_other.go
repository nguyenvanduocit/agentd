//go:build !unix

package acpauth

import "io/fs"

// ownedByUser: Windows keeps its files in the user's profile, which other users cannot enter; there is no uid to compare.
func ownedByUser(fs.FileInfo) bool { return true }
