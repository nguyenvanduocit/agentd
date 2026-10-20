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

// dirVerdict is what the daemon makes of home/acp.
type dirVerdict int

const (
	dirPrivate dirVerdict = iota // a directory of this user that nobody else can enter
	dirLoose                     // a directory of this user that others can enter: tighten it
	dirForeign                   // not a directory (a link in its place), or another user's: what would be written lands elsewhere
)

// judgeDir is the verdict on a directory by its own file information. It is a pure function of that, and of the user
// and the kind of system, so that every case can be tested without a second user.
func judgeDir(fi fs.FileInfo, uid int, posix bool) dirVerdict {
	switch {
	case !fi.IsDir() || !ownedBy(fi, uid):
		return dirForeign
	case posix && fi.Mode().Perm()&0o077 != 0:
		return dirLoose
	}
	return dirPrivate
}

// judgeFile is whether the open file is one the daemon wrote: a regular file of this user that no one else can read or
// change. A link, a named pipe, a directory, another user's file or a file with a mode for group or others is not.
func judgeFile(fi fs.FileInfo, uid int, posix bool) bool {
	return fi.Mode().IsRegular() && ownedBy(fi, uid) && !(posix && fi.Mode().Perm()&0o077 != 0)
}

// checkDir is whether home/acp is a directory of this user that nobody else can enter. A symbolic link in its place is
// refused, not followed: what would be written through it lands elsewhere. With fix a directory that others could
// enter is tightened instead of refused.
func checkDir(dir string, fix bool) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	switch judgeDir(fi, currentUID(), posix()) {
	case dirForeign:
		return fmt.Errorf("%w: %s", ErrInsecure, dir)
	case dirLoose:
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

// The seams of the tests, nothing else sets them: openToken is how the token file is opened (so that a test can make
// the system fail), and afterOpen runs between the open and the judging of what was opened (so that a test can swap the
// name for something else at the worst moment).
var (
	openToken = openPrivate
	afterOpen = func() {}
)

// read is the token in the file. An absent file is fs.ErrNotExist; anything the daemon would not have written (a link,
// another owner, a mode others can read, a file that cannot be read by its owner, something that is not a token) is
// ErrInsecure, and Ensure replaces it. Any other error is the system failing (no descriptors left, an I/O error): it
// says nothing about the file, and Ensure does not answer it by replacing the token.
//
// What is judged is the open file, by its own information (f.Stat), never the name: between a look at a name and an
// open of it a rotation can rename a new file onto the name, and that is not a reason to distrust either file.
func read(home string) (string, error) {
	if err := checkDir(Dir(home), false); err != nil {
		return "", err
	}
	path := TokenFile(home)
	f, err := openToken(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("%w: %s cannot be read by its owner", ErrInsecure, path)
		}
		return "", err
	}
	defer f.Close()
	afterOpen()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !judgeFile(fi, currentUID(), posix()) {
		return "", fmt.Errorf("%w: %s", ErrInsecure, path)
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
// was configured with keeps working across restarts, and so it is when the system fails to read it (no descriptors
// left, an I/O error): the error is returned and the token is not replaced. The directory comes first: one that others
// could enter is tightened, which says nothing against the token in it. It fails when the directory is a link or
// another user's.
func Ensure(home string) (string, error) {
	if err := prepare(home); err != nil {
		return "", err
	}
	token, err := read(home)
	if err == nil {
		return token, nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrInsecure) {
		return Rotate(home)
	}
	return "", err
}

// Rotate makes a new token and puts it in the file: the old one stops working at once, for the daemon reads the file
// at each request.
func Rotate(home string) (string, error) {
	if err := prepare(home); err != nil {
		return "", err
	}
	if err := clearDirectoryAt(TokenFile(home)); err != nil {
		return "", err
	}
	token := New()
	if err := writeAtomic(TokenFile(home), []byte(token+"\n")); err != nil {
		return "", err
	}
	return token, nil
}

// clearDirectoryAt removes an empty directory that stands where a file is to be put: a rename cannot replace it. A
// file, a link or a pipe there the rename replaces; a directory that holds something is not the daemon's to delete, and
// is an error.
func clearDirectoryAt(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("%w: %s is a directory that holds something", ErrInsecure, path)
		}
	}
	return nil
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
	if err := renameInto(tmp, path); err != nil {
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
	if err := clearDirectoryAt(InfoFile(home)); err != nil {
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

// RemoveInfo takes the record away whoever's it is; there being none is not an error. A daemon that ended takes away
// its own with RemoveInfoOf.
func RemoveInfo(home string) error {
	if err := os.Remove(InfoFile(home)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveInfoOf takes the record away only if it names address: the daemon that ended takes away its own, and leaves
// that of another daemon on the same home (agentd.app beside a terminal `agentd run`) where it is. A record that is
// not there, or that cannot be read, is not an address of this daemon and is left alone.
func RemoveInfoOf(home, address string) error {
	info, err := ReadInfo(home)
	if err != nil || info.Address != address {
		return nil
	}
	return RemoveInfo(home)
}

// Address is the ready-made address of one agent: the endpoint's address (with or without a final slash), the agent,
// and the token in the query. The token is URL-safe text and needs no escaping.
func Address(base, agent, token string) string {
	return strings.TrimSuffix(base, "/") + "/" + agent + "?token=" + token
}
