package consent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Connection is one TCP connection to the gateway, as the gateway's socket sees it. Both addresses are IPv4 loopback.
type Connection struct {
	PeerAddress    string
	PeerPort       int
	GatewayAddress string
	GatewayPort    int
}

const (
	timeWait     = "06"
	mappedPrefix = "0000000000000000FFFF0000"
)

var (
	uidPattern  = regexp.MustCompile(`^[0-9]+$`)
	octetFormat = regexp.MustCompile(`^[0-9]{1,3}$`)
)

// procHex is an IPv4 address as /proc/net/tcp writes it: the four bytes in host order (little-endian on x86-64 and
// arm64). It is false for anything that is not a dotted IPv4 address.
func procHex(address string) (string, bool) {
	parts := strings.Split(address, ".")
	if len(parts) != 4 {
		return "", false
	}
	var out [4]byte
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if !octetFormat.MatchString(p) || err != nil || n > 255 {
			return "", false
		}
		out[3-i] = byte(n)
	}
	return fmt.Sprintf("%02X%02X%02X%02X", out[0], out[1], out[2], out[3]), true
}

func procEnds(address string, port int) []string {
	hex, ok := procHex(address)
	if !ok {
		return nil
	}
	p := fmt.Sprintf("%04X", port)
	return []string{hex + ":" + p, mappedPrefix + hex + ":" + p}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ProcNetOwners are the uids of the peer's sockets of c in the text of /proc/net/tcp or /proc/net/tcp6: the rows whose
// local end is the peer and whose remote end is the gateway. A TIME_WAIT row belongs to no process (its uid reads 0)
// and is skipped.
func ProcNetOwners(text string, c Connection) []int {
	locals := procEnds(c.PeerAddress, c.PeerPort)
	remotes := procEnds(c.GatewayAddress, c.GatewayPort)
	var owners []int
	for line := range strings.SplitSeq(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		local, remote, state, uid := f[1], f[2], f[3], f[7]
		if !contains(locals, local) || !contains(remotes, remote) || state == timeWait || !uidPattern.MatchString(uid) {
			continue
		}
		if n, err := strconv.Atoi(uid); err == nil {
			owners = append(owners, n)
		}
	}
	return owners
}

// LsofArgs are the arguments of lsof that list every process holding a TCP socket on the peer's port, in its field
// output.
func LsofArgs(peerPort int) []string {
	return []string{"-nP", "-w", fmt.Sprintf("-iTCP:%d", peerPort), "-Fpun"}
}

// LsofOwners are the uids of the processes holding the peer's end of c, from `lsof -F pun` output: a `p` line starts a
// process, its `u` line is the uid, and each `n` line names one socket as `local->remote`.
func LsofOwners(text string, c Connection) []int {
	name := fmt.Sprintf("%s:%d->%s:%d", c.PeerAddress, c.PeerPort, c.GatewayAddress, c.GatewayPort)
	var owners []int
	uid, haveUID := 0, false
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			haveUID = false
		case 'u':
			n, err := strconv.Atoi(value)
			uid, haveUID = n, uidPattern.MatchString(value) && err == nil
		case 'n':
			if value == name && haveUID {
				owners = append(owners, uid)
			}
		}
	}
	return owners
}

// PeerAllowed lets a connection in only when its owners are known and every one of them is this process's user. me is
// the daemon's uid (os.Getuid); a negative me (Windows) is no known user, which refuses.
func PeerAllowed(owners []int, me int) bool {
	if me < 0 || len(owners) == 0 {
		return false
	}
	for _, u := range owners {
		if u != me {
			return false
		}
	}
	return true
}
