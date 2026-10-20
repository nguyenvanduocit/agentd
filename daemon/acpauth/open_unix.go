//go:build unix

package acpauth

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openPrivate opens the file at path for reading, and judges nothing: the caller looks at the open file itself
// (f.Stat), so that what is checked is what is read, with no window between a check by name and an open by name for a
// rename to land in. The open does not follow a link (a link at the name is refused as insecure) and does not wait for
// a writer: a named pipe in the file's place opens at once and is refused by its type.
func openPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) { // what O_NOFOLLOW answers for a link
			return nil, fmt.Errorf("%w: %s is a link", ErrInsecure, path)
		}
		return nil, err
	}
	return f, nil
}

// renameInto puts the file from over the file to.
func renameInto(from, to string) error { return os.Rename(from, to) }
