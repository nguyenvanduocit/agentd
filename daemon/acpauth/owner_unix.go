//go:build unix

package acpauth

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByUser is whether the file belongs to the user this process runs as: a file another user owns in a place the
// daemon expected to be private is not one it wrote.
func ownedByUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
