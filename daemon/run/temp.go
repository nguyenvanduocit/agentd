package run

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	tempPrefix = "agentd-"
	tempChars  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	tempSuffix = 6 // the shape proc.SessionState cleans: agentd- and six characters
	tempTries  = 100
)

// makeTempDirs makes the directory this run's MCP wrappers live in: `<tmp>/agentd-XXXXXX/bin` (proc.SessionState
// removes leftovers of that name). `root` is the daemon's own, mode 0700: the socket of the pages' bridges goes there
// too. os.MkdirTemp is not used: its suffix is not six characters.
func makeTempDirs() (root, bin string, err error) {
	for range tempTries {
		name, err := randomName()
		if err != nil {
			return "", "", err
		}
		root = filepath.Join(os.TempDir(), tempPrefix+name)
		err = os.Mkdir(root, 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("make the daemon's temporary directory: %w", err)
		}
		bin = filepath.Join(root, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return "", "", fmt.Errorf("make the daemon's temporary directory: %w", err)
		}
		return root, bin, nil
	}
	return "", "", fmt.Errorf("make the daemon's temporary directory: every name tried exists: %w", fs.ErrExist)
}

func randomName() (string, error) {
	b := make([]byte, tempSuffix)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make the daemon's temporary directory: %w", err)
	}
	for i := range b {
		b[i] = tempChars[int(b[i])%len(tempChars)]
	}
	return string(b), nil
}
