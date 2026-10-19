package consent

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// PeerOwners are the uids of the processes behind the peer's end of a connection. It fails when the lookup itself
// cannot run.
type PeerOwners func(c Connection) ([]int, error)

const lsofPath = "/usr/sbin/lsof"

// lsofLookup is macOS: lsof lists the processes holding a socket. It exits 1 and says nothing when no socket matches.
func lsofLookup(lsof string) PeerOwners {
	return func(c Connection) ([]int, error) {
		cmd := exec.Command(lsof, LsofArgs(c.PeerPort)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err == nil {
			return LsofOwners(stdout.String(), c), nil
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return nil, fmt.Errorf("%s: %w", lsof, err)
		}
		if ee.ExitCode() == 1 && stderr.Len() == 0 {
			return LsofOwners(stdout.String(), c), nil
		}
		return nil, fmt.Errorf("%s exited %d: %s", lsof, ee.ExitCode(), strings.TrimSpace(stderr.String()))
	}
}

// readProc reads a /proc/net table. /proc/net/tcp6 is missing on a kernel without IPv6.
func readProc(file string) (string, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) && strings.HasSuffix(file, "6") {
		return "", nil
	}
	return string(b), err
}

// procLookup is Linux: the kernel's socket tables carry the uid of each socket.
func procLookup(tcp, tcp6 string) PeerOwners {
	return func(c Connection) ([]int, error) {
		v4, err := readProc(tcp)
		if err != nil {
			return nil, err
		}
		v6, err := readProc(tcp6)
		if err != nil {
			return nil, err
		}
		return ProcNetOwners(v4+"\n"+v6, c), nil
	}
}

// SystemOwners asks the operating system: /proc/net/tcp and tcp6 on Linux, lsof elsewhere.
func SystemOwners() PeerOwners {
	if runtime.GOOS == "linux" {
		return procLookup("/proc/net/tcp", "/proc/net/tcp6")
	}
	return lsofLookup(lsofPath)
}

func connectionOf(local, remote net.Addr) (Connection, bool) {
	l, lok := local.(*net.TCPAddr)
	r, rok := remote.(*net.TCPAddr)
	if !lok || !rok || l == nil || r == nil || l.IP == nil || r.IP == nil {
		return Connection{}, false
	}
	return Connection{PeerAddress: r.IP.String(), PeerPort: r.Port, GatewayAddress: l.IP.String(), GatewayPort: l.Port}, true
}

// PeerGate decides whether a connection comes from a process of this OS user, once per connection (keep-alive requests
// and the upgrade that follows share the answer): the gateway calls Forget when a connection closes. An owner that
// cannot be found is a no; a lookup that fails is a no that is logged once, so a broken lookup shows in the log rather
// than only as refused pages.
type PeerGate struct {
	owners PeerOwners
	me     int
	warn   func(message string)

	mu      sync.Mutex
	answers map[net.Conn]*answer
	warned  bool
}

type answer struct {
	once    sync.Once
	allowed bool
}

// NewPeerGate is a gate for the daemon whose OS user is me (os.Getuid; negative when there is none) that looks owners
// up with owners (SystemOwners) and reports a failed lookup to warn.
func NewPeerGate(owners PeerOwners, me int, warn func(message string)) *PeerGate {
	return &PeerGate{owners: owners, me: me, warn: warn, answers: map[net.Conn]*answer{}}
}

// Allowed is whether conn comes from a process of this OS user.
func (g *PeerGate) Allowed(conn net.Conn) bool {
	g.mu.Lock()
	a, known := g.answers[conn]
	if !known {
		a = &answer{}
		g.answers[conn] = a
	}
	g.mu.Unlock()
	a.once.Do(func() { a.allowed = g.decide(conn) })
	return a.allowed
}

// Forget drops what the gate knows of conn; the gateway calls it when the connection closes.
func (g *PeerGate) Forget(conn net.Conn) {
	g.mu.Lock()
	delete(g.answers, conn)
	g.mu.Unlock()
}

func (g *PeerGate) decide(conn net.Conn) bool {
	c, ok := connectionOf(conn.LocalAddr(), conn.RemoteAddr())
	if !ok {
		return false
	}
	owners, err := g.owners(c)
	if err != nil {
		g.mu.Lock()
		first := !g.warned
		g.warned = true
		g.mu.Unlock()
		if first {
			g.warn("local gateway: cannot tell which user a connection comes from, refusing it: " + err.Error())
		}
		return false
	}
	return PeerAllowed(owners, g.me)
}
