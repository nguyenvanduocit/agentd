//go:build unix

package acpauth

import (
	"io/fs"
	"syscall"
	"testing"
	"time"
)

// info is a file as the system describes it, with the owner we choose: a second user cannot be had in a test, and the
// decisions about owner, mode and type are pure functions of this.
type info struct {
	mode fs.FileMode
	uid  uint32
}

func (i info) Name() string       { return "x" }
func (i info) Size() int64        { return 0 }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.mode.IsDir() }
func (i info) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestAFileIsTheDaemonsOnlyIfItIsARegularPrivateFileOfThisUser(t *testing.T) {
	const me = 1000
	for name, c := range map[string]struct {
		fi    info
		posix bool
		want  bool
	}{
		"a regular 0600 file of mine":      {info{0o600, me}, true, true},
		"a regular 0400 file of mine":      {info{0o400, me}, true, true},
		"another user's 0600 file":         {info{0o600, me + 1}, true, false},
		"root's 0600 file":                 {info{0o600, 0}, true, false},
		"my file readable by the group":    {info{0o640, me}, true, false},
		"my file readable by others":       {info{0o604, me}, true, false},
		"my file writable by the group":    {info{0o620, me}, true, false},
		"a named pipe of mine":             {info{fs.ModeNamedPipe | 0o600, me}, true, false},
		"a symbolic link of mine":          {info{fs.ModeSymlink | 0o600, me}, true, false},
		"a directory of mine":              {info{fs.ModeDir | 0o700, me}, true, false},
		"a socket of mine":                 {info{fs.ModeSocket | 0o600, me}, true, false},
		"a device of mine":                 {info{fs.ModeDevice | 0o600, me}, true, false},
		"modes mean nothing there (0666)":  {info{0o666, me}, false, true},
		"modes mean nothing there, a pipe": {info{fs.ModeNamedPipe | 0o666, me}, false, false},
	} {
		if got := judgeFile(c.fi, me, c.posix); got != c.want {
			t.Errorf("%s: judgeFile = %v, want %v", name, got, c.want)
		}
	}
}

func TestADirectoryIsPrivateLooseOrForeign(t *testing.T) {
	const me = 1000
	for name, c := range map[string]struct {
		fi    info
		posix bool
		want  dirVerdict
	}{
		"mine, 0700":                    {info{fs.ModeDir | 0o700, me}, true, dirPrivate},
		"mine, 0755":                    {info{fs.ModeDir | 0o755, me}, true, dirLoose},
		"mine, 0710":                    {info{fs.ModeDir | 0o710, me}, true, dirLoose},
		"mine, 0770":                    {info{fs.ModeDir | 0o770, me}, true, dirLoose},
		"another user's, 0700":          {info{fs.ModeDir | 0o700, me + 1}, true, dirForeign},
		"another user's, 0755":          {info{fs.ModeDir | 0o755, me + 1}, true, dirForeign},
		"root's, 0700":                  {info{fs.ModeDir | 0o700, 0}, true, dirForeign},
		"a symbolic link of mine":       {info{fs.ModeSymlink | 0o777, me}, true, dirForeign},
		"a regular file of mine":        {info{0o700, me}, true, dirForeign},
		"modes mean nothing there":      {info{fs.ModeDir | 0o777, me}, false, dirPrivate},
		"modes mean nothing, another's": {info{fs.ModeDir | 0o777, me + 1}, false, dirForeign},
	} {
		if got := judgeDir(c.fi, me, c.posix); got != c.want {
			t.Errorf("%s: judgeDir = %v, want %v", name, got, c.want)
		}
	}
}
