package acpauth

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
	"github.com/nguyenvanduocit/agentd/daemon/cli"
)

// DefaultAddress is where the endpoint is when agentd has not recorded another: the first port of the gateway's range.
const DefaultAddress = "ws://127.0.0.1:47615/acp"

// answerWithin is how long RunACP waits to learn whether agentd answers on the recorded port.
const answerWithin = 300 * time.Millisecond

// Deps is what `agentd acp` touches.
type Deps struct {
	// Home is agentd's own home, where the token and the record are.
	Home           string
	Stdout, Stderr io.Writer
	// Dial tries a TCP connection to addr; nil dials for real.
	Dial func(addr string, timeout time.Duration) error
}

func (d Deps) dial(addr string) error {
	if d.Dial != nil {
		return d.Dial(addr, answerWithin)
	}
	conn, err := net.DialTimeout("tcp", addr, answerWithin)
	if err != nil {
		return err
	}
	return conn.Close()
}

// RunACP is `agentd acp [<agent>] [--rotate] [--token]`: it prints the ready-made address of each agent that is ready
// (or of the one named), for a program that speaks ACP over a WebSocket; or, with --token, the token alone. It returns
// the exit code. The addresses go to stdout, one a line, and everything else to stderr, so that the output can be used
// as it stands; the token is on stdout only, and never in a hint.
func RunACP(cmd cli.ACP, d Deps) int {
	if cmd.Agent != "" && !agents.IsAgentName(cmd.Agent) {
		fmt.Fprintf(d.Stderr, "unknown agent %q: choose %s\n", cmd.Agent, agentChoices())
		return 1
	}
	token, code := tokenFor(cmd, d)
	if code != 0 {
		return code
	}
	if cmd.Token {
		fmt.Fprintln(d.Stdout, token)
		return 0
	}

	base, ready, known := DefaultAddress, allAgents(), false
	if info, err := ReadInfo(d.Home); err == nil && info.Address != "" {
		base, ready, known = info.Address, info.Agents, true
		if host := hostOf(base); host != "" {
			if err := d.dial(host); err != nil {
				fmt.Fprintf(d.Stderr, "agentd is not running on port %s (the address is the one it used last): start agentd, then use it\n", portOf(host))
			}
		}
	} else {
		fmt.Fprintf(d.Stderr, "agentd is not running: the address assumes its first port, %s, and every agent; start agentd and run `agentd acp` again to see which agents are ready\n", portOf(hostOf(DefaultAddress)))
	}

	var names []string
	switch {
	case cmd.Agent != "":
		names = []string{cmd.Agent}
		if known && !contains(ready, cmd.Agent) {
			fmt.Fprintf(d.Stderr, "%s is not ready right now; the address works once it is\n", cmd.Agent)
		}
	default:
		names = ready
	}
	if len(names) == 0 {
		fmt.Fprintln(d.Stderr, "no agent is ready yet: agentd sets them up in the background, the first time it runs; try again in a minute")
		return 1
	}
	for _, name := range names {
		fmt.Fprintln(d.Stdout, Address(base, name, token))
	}
	fmt.Fprintln(d.Stderr, `the header form is preferred where the app supports it: send the token as "Authorization: Bearer <token>" instead (agentd acp --token prints the token)`)
	return 0
}

// tokenFor is the token to print, made new first with --rotate; the exit code is not 0 when there is none to give.
func tokenFor(cmd cli.ACP, d Deps) (token string, code int) {
	var err error
	if cmd.Rotate {
		token, err = Rotate(d.Home)
	} else {
		token, err = Load(d.Home)
	}
	switch {
	case err == nil:
		return token, 0
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(d.Stderr, "agentd has not run on this computer yet, so there is no token to give: start agentd, then run `agentd acp` again")
	case errors.Is(err, ErrInsecure):
		fmt.Fprintf(d.Stderr, "the token file cannot be trusted (%v): run `agentd acp --rotate` to make a new one\n", err)
	default:
		fmt.Fprintf(d.Stderr, "cannot read the token: %v\n", err)
	}
	return "", 1
}

func agentChoices() string {
	names := make([]string, len(agents.AgentNames))
	for i, n := range agents.AgentNames {
		names[i] = string(n)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

func allAgents() []string {
	names := make([]string, len(agents.AgentNames))
	for i, n := range agents.AgentNames {
		names[i] = string(n)
	}
	return names
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// hostOf is the host:port of a ws:// address; empty when it does not have one.
func hostOf(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Host
}

func portOf(host string) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	return host
}
