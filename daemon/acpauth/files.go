package acpauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
)

// The files, under <home>/acp/ (home is agentd's own, ~/.agentd): the token, and the address of the endpoint that the
// running daemon records for `agentd acp`. The directory is mode 0700 and the files 0600, so that only the user's own
// processes on the host can read them; a container or a sandbox that does not mount the home cannot.
const (
	dirName   = "acp"
	tokenName = "token"
	infoName  = "acp.json"

	// maxFile bounds what is read: a token is 43 bytes and the info file a few hundred.
	maxFile = 64 << 10
)

// Dir is the directory of the files.
func Dir(home string) string { return filepath.Join(home, dirName) }

// TokenFile is the file that holds the token.
func TokenFile(home string) string { return filepath.Join(Dir(home), tokenName) }

// InfoFile is the file that holds the address of the endpoint (never the token).
func InfoFile(home string) string { return filepath.Join(Dir(home), infoName) }

// ErrInsecure is the reason a file or directory is not used: it is not what the daemon wrote, or others could have
// read or changed it.
var ErrInsecure = errors.New("acpauth: the file is not private to this user")

var writes atomic.Uint64

// checkDir is whether home/acp is a directory of this user that nobody else can enter. A symbolic link in its place is
// refused, not followed: what would be written through it lands elsewhere.
func checkDir(dir string, fix bool) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || !ownedByUser(fi) {
		return fmt.Errorf("%w: %s", ErrInsecure, dir)
	}
	if posix() && fi.Mode().Perm()&0o077 != 0 {
		if !fix {
			return fmt.Errorf("%w: %s can be entered by others", ErrInsecure, dir)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func posix() bool { return runtime.GOOS != "windows" }

// prepare makes home/acp as a private directory.
func prepare(home string) error {
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return checkDir(dir, true)
}

// read is the token in the file. An absent file is fs.ErrNotExist; anything the daemon would not have written (a link,
// another owner, a mode others can read, something that is not a token) is ErrInsecure, and the callers replace it.
func read(home string) (string, error) {
	if err := checkDir(Dir(home), false); err != nil {
		return "", err
	}
	path := TokenFile(home)
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || !ownedByUser(before) || (posix() && before.Mode().Perm()&0o077 != 0) {
		return "", fmt.Errorf("%w: %s", ErrInsecure, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if after, err := f.Stat(); err != nil || !os.SameFile(before, after) {
		return "", fmt.Errorf("%w: %s changed while it was read", ErrInsecure, path) // swapped between the check and the open
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFile))
	if err != nil {
		return "", err
	}
	token := strings.TrimRight(string(data), "\r\n")
	if !Valid(token) {
		return "", fmt.Errorf("%w: %s does not hold a token", ErrInsecure, path)
	}
	return token, nil
}

// Load is the token in the file of home; fs.ErrNotExist when there is none, and an error when the file is not one the
// daemon wrote. It creates nothing.
func Load(home string) (string, error) { return read(home) }

// Ensure is the token of home, made when there is none or when the file cannot be trusted: a link, a file others could
// read, an empty file or one that holds anything but a token. A good one is left as it is, so that an address a client
// was configured with keeps working across restarts. It fails when the directory itself is not private to this user.
func Ensure(home string) (string, error) {
	if token, err := read(home); err == nil {
		return token, nil
	}
	return Rotate(home)
}

// Rotate makes a new token and puts it in the file: the old one stops working at once, for the daemon reads the file
// at each request.
func Rotate(home string) (string, error) {
	if err := prepare(home); err != nil {
		return "", err
	}
	token := New()
	if err := writeAtomic(TokenFile(home), []byte(token+"\n")); err != nil {
		return "", err
	}
	return token, nil
}

// writeAtomic writes the file through a temporary file in the same directory and a rename, so that a reader sees the
// old content or the new and never half of it. The temporary file is created exclusively with mode 0600: a name an
// attacker planted is not followed, and a symbolic link at the final name is replaced, not written through.
func writeAtomic(path string, data []byte) error {
	tmp := path + "." + strconv.Itoa(os.Getpid()) + "." + strconv.FormatUint(writes.Add(1), 10) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Info is what the running daemon records for `agentd acp`: where /acp is and which agents are ready. It never holds the
// token, which has its own file.
type Info struct {
	// Address is the endpoint without an agent: ws://127.0.0.1:<port>/acp.
	Address string `json:"address"`
	// Agents are the agents that are ready now.
	Agents []string `json:"agents"`
	// Updated is when it was written (RFC 3339).
	Updated string `json:"updated"`
}

// WriteInfo records the address of the endpoint.
func WriteInfo(home string, info Info) error {
	if err := prepare(home); err != nil {
		return err
	}
	if info.Agents == nil {
		info.Agents = []string{}
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return writeAtomic(InfoFile(home), append(data, '\n'))
}

// ReadInfo is what the daemon recorded; fs.ErrNotExist when it has not, or when it ended and took the record away.
func ReadInfo(home string) (Info, error) {
	if err := checkDir(Dir(home), false); err != nil {
		return Info{}, err
	}
	f, err := os.Open(InfoFile(home))
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFile))
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("acpauth: %s: %w", InfoFile(home), err)
	}
	return info, nil
}

// RemoveInfo takes the record away (the daemon ended); there being none is not an error.
func RemoveInfo(home string) error {
	if err := os.Remove(InfoFile(home)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Address is the ready-made address of one agent: the endpoint's address (with or without a final slash), the agent,
// and the token in the query. The token is URL-safe text and needs no escaping.
func Address(base, agent, token string) string {
	return strings.TrimSuffix(base, "/") + "/" + agent + "?token=" + token
}
