package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// File is the registry file: --config when given, else the default location of the host.
func File(config *string, h home.Host) string {
	if config != nil {
		return *config
	}
	return home.ConfigFile(h)
}

var writes atomic.Uint64

// Save writes the registry through a temporary file and a rename: the daemon and `pair` both write it, and a reader
// never sees half of it. The file is mode 0600 in a directory of 0700 (when Save creates it).
func Save(file string, r Registry) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + "." + strconv.Itoa(os.Getpid()) + "." + strconv.FormatUint(writes.Add(1), 10) + ".tmp"
	if err := os.WriteFile(tmp, r.Marshal(), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil { // WriteFile keeps the mode of a file that existed
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Load reads the apps and browser grants saved in file. A missing file is an empty registry: a daemon with nothing
// paired waits for an app to ask.
func Load(file string) (Registry, error) {
	text, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("%s: %w", file, err)
	}
	r, err := Parse(string(text))
	if err != nil {
		return Registry{}, fmt.Errorf("%s %w", file, err)
	}
	return r, nil
}

// One change at a time per file within this process: the gateway, the tray and `pair` all change the registry, and
// two read-modify-write cycles at once would lose one of them.
var locks sync.Map // file -> *sync.Mutex

// Update loads the registry, applies change and saves the result, after every earlier change of file has finished.
// It returns what was saved.
func Update(file string, change func(Registry) Registry) (Registry, error) {
	lock, _ := locks.LoadOrStore(file, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	cur, err := Load(file)
	if err != nil {
		return Registry{}, err
	}
	next := change(cur)
	if err := Save(file, next); err != nil {
		return Registry{}, err
	}
	return next, nil
}
