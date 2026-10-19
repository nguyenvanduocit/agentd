package consent

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Captured on Linux (oven/bun:1, uid 1234): a server on 127.0.0.1:40473 (0x9E19), an IPv4 client from port 59858
// (0xE9D2) and an IPv6 client to ::ffff:127.0.0.1 from port 59870 (0xE9DE).
const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:9E19 00000000:0000 0A 00000000:00000001 00:00000000 00000000  1234        0 4981 2 00000000cdc04366 100 0 0 10 0
   1: 0100007F:E9D2 0100007F:9E19 01 00000000:00000000 00:00000000 00000000  1234        0 4982 2 00000000dc8c8623 20 0 0 10 -1
   2: 0100007F:9E19 0100007F:E9DE 01 00000000:00000000 00:00000000 00000000  1234        0 0 1 00000000637eb0b8 20 0 0 10 -1
   3: 0100007F:9E19 0100007F:E9D2 01 00000000:00000000 00:00000000 00000000  1234        0 9411 1 000000002b8b53c1 20 0 0 10 -1
`
const procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF00000100007F:E9DE 0000000000000000FFFF00000100007F:9E19 01 00000000:00000000 00:00000000 00000000  1234        0 4983 2 000000005753e3de 20 0 0 10 -1
`

// Captured on macOS with `lsof -nP -w -iTCP:61811 -Fpun`: the gateway (pid 31070) on 127.0.0.1:61810 and a client
// (python, pid 31071) from port 61811.
const lsofText = "p31070\nu501\nf5\nn127.0.0.1:61810->127.0.0.1:61811\np31071\nu501\nf3\nn127.0.0.1:61811->127.0.0.1:61810\n"

func linux(peerPort int) Connection {
	return Connection{PeerAddress: "127.0.0.1", PeerPort: peerPort, GatewayAddress: "127.0.0.1", GatewayPort: 40473}
}

var mac = Connection{PeerAddress: "127.0.0.1", PeerPort: 61811, GatewayAddress: "127.0.0.1", GatewayPort: 61810}

func wantOwners(t *testing.T, got []int, want ...int) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("owners = %v, want %v", got, want)
	}
}

func TestProcNetOwnersThePeersOwnSocketNotTheGatewaysEnd(t *testing.T) {
	text := strings.Replace(procTCP, "  1234        0 4982", "  1000        0 4982", 1)
	wantOwners(t, ProcNetOwners(text, linux(59858)), 1000)
}

func TestProcNetOwnersAnIPv4MappedRowOfTcp6(t *testing.T) {
	text := strings.Replace(procTCP6, "1234", "1000", 1)
	wantOwners(t, ProcNetOwners(text, linux(59870)), 1000)
	wantOwners(t, ProcNetOwners(procTCP, linux(59870)))
}

func TestProcNetOwnersAnotherLoopbackSourceAddress(t *testing.T) {
	text := strings.Replace(procTCP, "0100007F:E9D2 0100007F:9E19", "0200007F:E9D2 0100007F:9E19", 1)
	c := linux(59858)
	c.PeerAddress = "127.0.0.2"
	wantOwners(t, ProcNetOwners(text, c), 1234)
	wantOwners(t, ProcNetOwners(text, linux(59858)))
}

func TestProcNetOwnersNoRowTimeWaitMalformedAndNonIPv4GiveNoOwner(t *testing.T) {
	wantOwners(t, ProcNetOwners(procTCP, linux(1)))
	timeWaitRow := strings.Replace(procTCP, "0100007F:9E19 01 00000000:00000000 00:00000000 00000000  1234        0 4982", "0100007F:9E19 06 00000000:00000000 00:00000000 00000000     0        0 4982", 1)
	wantOwners(t, ProcNetOwners(timeWaitRow, linux(59858)))
	wantOwners(t, ProcNetOwners("garbage\n   1: 0100007F:E9D2 0100007F:9E19 01 x x x x\n   1: 0100007F:E9D2\n", linux(59858)))
	wantOwners(t, ProcNetOwners("", linux(59858)))
	for _, addr := range []string{"::1", "127.0.0.", "1.2.3.4.5", "256.0.0.1", "a.b.c.d", ""} {
		c := linux(59858)
		c.PeerAddress = addr
		wantOwners(t, ProcNetOwners(procTCP, c))
	}
}

func TestLsofOwnersTheUidOfTheProcessThatHoldsThePeersEnd(t *testing.T) {
	wantOwners(t, LsofOwners(strings.Replace(lsofText, "p31071\nu501", "p31071\nu502", 1), mac), 502)
}

func TestLsofOwnersEveryProcessHoldingTheSocketCounts(t *testing.T) {
	wantOwners(t, LsofOwners(lsofText+"p31072\nu0\nf4\nn127.0.0.1:61811->127.0.0.1:61810\n", mac), 501, 0)
}

func TestLsofOwnersNoSocketEmptyAndMalformedGiveNoOwner(t *testing.T) {
	other := mac
	other.PeerPort = 1
	wantOwners(t, LsofOwners(lsofText, other))
	other = mac
	other.GatewayPort = 1
	wantOwners(t, LsofOwners(lsofText, other))
	wantOwners(t, LsofOwners("", mac))
	wantOwners(t, LsofOwners("n127.0.0.1:61811->127.0.0.1:61810\n", mac))
	wantOwners(t, LsofOwners("p1\nuroot\nn127.0.0.1:61811->127.0.0.1:61810\n", mac))
}

func TestLsofArgsTheTCPSocketsOfThePeersPortOnly(t *testing.T) {
	if got := LsofArgs(61811); !slices.Equal(got, []string{"-nP", "-w", "-iTCP:61811", "-Fpun"}) {
		t.Errorf("args = %q", got)
	}
}

func TestPeerAllowed(t *testing.T) {
	cases := []struct {
		owners []int
		me     int
		want   bool
	}{
		{[]int{501}, 501, true},
		{[]int{501, 501}, 501, true},
		{[]int{502}, 501, false},
		{[]int{501, 0}, 501, false},
		{nil, 501, false},
		{[]int{501}, -1, false},
	}
	for _, c := range cases {
		if got := PeerAllowed(c.owners, c.me); got != c.want {
			t.Errorf("PeerAllowed(%v, %d) = %v", c.owners, c.me, got)
		}
	}
}

// conn is a connection with the given addresses; only the addresses are used.
type conn struct {
	net.Conn
	local, remote net.Addr
}

func (c *conn) LocalAddr() net.Addr  { return c.local }
func (c *conn) RemoteAddr() net.Addr { return c.remote }

func socket(peerPort int) *conn {
	ip := net.IPv4(127, 0, 0, 1)
	return &conn{local: &net.TCPAddr{IP: ip, Port: 47615}, remote: &net.TCPAddr{IP: ip, Port: peerPort}}
}

func TestPeerGateAsksOncePerConnection(t *testing.T) {
	var asked []Connection
	gate := NewPeerGate(func(c Connection) ([]int, error) {
		asked = append(asked, c)
		return []int{501}, nil
	}, 501, func(string) {})
	a := socket(50000)
	for range 2 {
		if !gate.Allowed(a) {
			t.Fatal("refused")
		}
	}
	if !gate.Allowed(socket(50001)) {
		t.Fatal("refused")
	}
	want := []Connection{
		{PeerAddress: "127.0.0.1", PeerPort: 50000, GatewayAddress: "127.0.0.1", GatewayPort: 47615},
		{PeerAddress: "127.0.0.1", PeerPort: 50001, GatewayAddress: "127.0.0.1", GatewayPort: 47615},
	}
	if !slices.Equal(asked, want) {
		t.Errorf("asked = %+v", asked)
	}
	gate.Forget(a)
	gate.Allowed(a)
	if len(asked) != 3 {
		t.Errorf("a forgotten connection is asked again: %d lookups", len(asked))
	}
}

func TestPeerGateAnotherUserAClosedSocketAndAFailingLookupAreRefusedTheFailureLoggedOnce(t *testing.T) {
	var logged []string
	warn := func(m string) { logged = append(logged, m) }
	if NewPeerGate(func(Connection) ([]int, error) { return []int{502}, nil }, 501, warn).Allowed(socket(50000)) {
		t.Error("another user's process let in")
	}
	if NewPeerGate(func(Connection) ([]int, error) { return []int{501}, nil }, 501, warn).Allowed(&conn{}) {
		t.Error("a socket without addresses let in")
	}
	broken := NewPeerGate(func(Connection) ([]int, error) { return nil, errors.New("lsof is gone") }, 501, warn)
	if broken.Allowed(socket(50000)) || broken.Allowed(socket(50001)) {
		t.Error("a failing lookup let in")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "lsof is gone") {
		t.Errorf("logged = %q", logged)
	}
}

func TestProcLookupReadsTheTablesAndToleratesAMissingTcp6(t *testing.T) {
	dir := t.TempDir()
	tcp, tcp6 := filepath.Join(dir, "tcp"), filepath.Join(dir, "tcp6")
	if err := os.WriteFile(tcp, []byte(strings.Replace(procTCP, "  1234        0 4982", "  1000        0 4982", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := procLookup(tcp, tcp6)(linux(59858))
	if err != nil {
		t.Fatal(err)
	}
	wantOwners(t, got, 1000)
	if _, err := procLookup(filepath.Join(dir, "missing"), tcp6)(linux(59858)); err == nil {
		t.Error("a missing /proc/net/tcp is an error")
	}
}

func TestLsofLookupExitOneAndSilentIsNoOwnerOtherFailuresError(t *testing.T) {
	dir := t.TempDir()
	if got, err := lsofLookup(script(t, dir, "lsof-ok", "printf 'p1\\nu501\\nn127.0.0.1:61811->127.0.0.1:61810\\n'"))(mac); err != nil {
		t.Fatal(err)
	} else {
		wantOwners(t, got, 501)
	}
	if got, err := lsofLookup(script(t, dir, "lsof-none", "exit 1"))(mac); err != nil {
		t.Fatal(err)
	} else {
		wantOwners(t, got)
	}
	if _, err := lsofLookup(script(t, dir, "lsof-noisy", "echo boom >&2\nexit 1"))(mac); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if _, err := lsofLookup(filepath.Join(dir, "missing"))(mac); err == nil {
		t.Error("a missing lsof is an error")
	}
}
