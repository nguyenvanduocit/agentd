//go:build unix

package acpauth

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedBy is whether the file belongs to the user uid: a file another user owns in a place the daemon expected to be
// private is not one it wrote.
func ownedBy(fi fs.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// currentUID is the user this process runs as.
func currentUID() int { return os.Getuid() }
