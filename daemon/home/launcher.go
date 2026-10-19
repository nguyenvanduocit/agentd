package home

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RefreshLauncher puts the launcher that ships beside the `agentd` binary (launcher.sh in scriptDir) at ~/.agentd/bin/agentd
// when that file differs. A new version can fix the launcher; the file is replaced by rename, so a running launcher
// keeps its copy. It reports whether it wrote.
func RefreshLauncher(scriptDir string, l Layout) (bool, error) {
	text, err := os.ReadFile(filepath.Join(scriptDir, "launcher.sh"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	have, err := os.ReadFile(l.Launcher)
	if err == nil && bytes.Equal(have, text) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(l.Launcher), 0o777); err != nil {
		return false, err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", l.Launcher, os.Getpid())
	if err := os.WriteFile(tmp, text, 0o755); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil { // WriteFile's mode goes through the umask
		_ = os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, l.Launcher); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}
