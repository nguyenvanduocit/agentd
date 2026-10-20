//go:build !unix

package acpauth

import (
	"fmt"
	"os"
	"time"
)

// openPrivate opens the file at path for reading. Windows has no O_NOFOLLOW, so the name is looked at first (a link, a
// reparse point or a directory is refused) and the open file must be the file that was looked at; a rename that lands in
// between (a rotation) is not an attack, so the open is tried again before it fails with an error that is not
// ErrInsecure, which a caller does not answer by replacing the token.
func openPrivate(path string) (*os.File, error) {
	var changed error
	for attempt := 0; attempt < 3; attempt++ {
		before, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !before.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s", ErrInsecure, path)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if after, err := f.Stat(); err == nil && os.SameFile(before, after) {
			return f, nil
		}
		f.Close()
		changed = fmt.Errorf("acpauth: %s was replaced while it was opened", path)
	}
	return nil, changed
}

// renameInto puts the file from over the file to. Go opens files without FILE_SHARE_DELETE, so while a reader holds the
// token file open (the daemon reads it at every request) the rename can be refused; it is tried again for a moment.
func renameInto(from, to string) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}
