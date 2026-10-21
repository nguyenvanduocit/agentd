package run

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// What the daemon keeps for the standard ACP endpoint (/acp, daemon/gateway/acp.go): the token a program on this
// computer must present (acpauth), and a record of where the endpoint is and which agents are ready, which
// `agentd acp` reads. The token is never logged, never put in an error and never in the record.

// acpRecordEvery is how often the record of where the endpoint is is looked at: if it went away (another daemon on
// this home ended and took it), the daemon puts it back.
const acpRecordEvery = 10 * time.Second

// acpState is the daemon's side of it.
type acpState struct {
	home string // agentd's own home: where the token and the record are
	// port is the gateway's port, 0 until it listens and while it is off. The adapter store tells of a change on a
	// goroutine of its own, so the port is not read from d.gateway.
	port atomic.Int32

	// mu orders the writing and the removing of the record, and guards problem.
	mu      sync.Mutex
	problem string // the last problem with the token that was told, "" while there is none
}

// setupAcp makes the token, once, so that it is there before the first program asks. A daemon that cannot make it
// goes on: web pages do not use it, and no program gets in without it.
func (d *daemon) setupAcp() {
	d.acp0.home = home.AgentdHome(d.Host)
	if _, err := acpauth.Ensure(d.acp0.home); err != nil {
		d.tellAcpProblem(err)
	}
	d.every(acpRecordEvery, d.healAcpInfo)
}

// acpToken is the token now, for the gateway to ask at each request: the file is the truth, so a new token (`agentd acp
// --rotate`) works at once. A file that cannot be trusted is replaced; one that cannot be made or read gives none, and
// then no program gets in.
func (d *daemon) acpToken() string {
	token, err := acpauth.Ensure(d.acp0.home)
	if err != nil {
		d.tellAcpProblem(err)
		return ""
	}
	d.clearAcpProblem()
	return token
}

// tellAcpProblem logs a problem with the token's files when it is not the one told last.
func (d *daemon) tellAcpProblem(err error) {
	text := err.Error()
	d.acp0.mu.Lock()
	same := d.acp0.problem == text
	d.acp0.problem = text
	d.acp0.mu.Unlock()
	if !same {
		d.Log.Warn("programs on this computer cannot use the standard ACP endpoint: " + text)
	}
}

// clearAcpProblem forgets the problem told last: the token works again, so the same problem coming back is news.
func (d *daemon) clearAcpProblem() {
	d.acp0.mu.Lock()
	d.acp0.problem = ""
	d.acp0.mu.Unlock()
}

// acpReadyAgents are the agents that are ready now, in the daemon's order.
func (d *daemon) acpReadyAgents() []string {
	var ready []string
	for _, a := range d.pageHello().Agents {
		if a.Ready {
			ready = append(ready, a.Name)
		}
	}
	return ready
}

// publishAcp says the gateway listens (or that it is off) and records where the endpoint is.
func (d *daemon) publishAcp() {
	if d.gateway != nil {
		d.acp0.port.Store(int32(d.gateway.Port))
	}
	d.writeAcpInfo()
}

// acpAddress is the address of the endpoint on a port, without an agent.
func acpAddress(port int) string { return fmt.Sprintf("ws://127.0.0.1:%d/acp", port) }

// writeAcpInfo records where the endpoint is and which agents are ready.
func (d *daemon) writeAcpInfo() { d.recordAcp(d.acpReadyAgents) }

// healAcpInfo puts the record back when it is not there, on a timer: another daemon on this home may have ended and
// taken it away. A record that is there is left, whoever's it is; this daemon rewrites it when its own ready agents
// change, not on a timer, so that two daemons on one home do not take turns overwriting.
func (d *daemon) healAcpInfo() { d.healAcp(d.acpReadyAgents) }

// recordAcp writes the record unless the file already says what this daemon would say: it is the file that is looked
// at, not a copy of what this daemon wrote, so a file that went away or names another port is put right. Without a
// gateway (every port taken, or the daemon ended) there is no endpoint, and no record.
func (d *daemon) recordAcp(agents func() []string) {
	d.acp0.mu.Lock()
	defer d.acp0.mu.Unlock()
	d.recordAcpLocked(agents)
}

// healAcp is recordAcp for a record that is not there.
func (d *daemon) healAcp(agents func() []string) {
	d.acp0.mu.Lock()
	defer d.acp0.mu.Unlock()
	if d.acp0.home == "" || d.acp0.port.Load() == 0 {
		return // no endpoint, and no place: an empty home would be a directory named acp in the working directory
	}
	if _, err := acpauth.ReadInfo(d.acp0.home); err == nil {
		return
	}
	d.recordAcpLocked(agents)
}

func (d *daemon) recordAcpLocked(agents func() []string) {
	port := int(d.acp0.port.Load())
	if port == 0 || d.acp0.home == "" {
		return
	}
	info := acpauth.Info{Address: acpAddress(port), Agents: agents()}
	if cur, err := acpauth.ReadInfo(d.acp0.home); err == nil && cur.Address == info.Address && slices.Equal(cur.Agents, info.Agents) {
		return
	}
	info.Updated = time.Now().UTC().Format(time.RFC3339)
	if err := acpauth.WriteInfo(d.acp0.home, info); err != nil {
		d.Log.Warn("cannot record where the standard ACP endpoint is: " + err.Error())
	}
}

// removeAcpInfo takes this daemon's record away: the endpoint is gone with it. The record of another daemon on the same
// home (agentd.app beside a terminal `agentd run`) is where it is.
func (d *daemon) removeAcpInfo() {
	d.acp0.mu.Lock()
	defer d.acp0.mu.Unlock()
	port := int(d.acp0.port.Swap(0))
	if port != 0 && d.acp0.home != "" {
		_ = acpauth.RemoveInfoOf(d.acp0.home, acpAddress(port))
	}
}

// acpAccess is the address of the standard ACP endpoint with its token, for the app to copy for the user. It travels in
// the state the app shows and is never logged. False while there is no endpoint (every
// port was taken) or no token.
func (d *daemon) acpAccess() (ui.AcpState, bool) {
	port := int(d.acp0.port.Load())
	if port == 0 {
		return ui.AcpState{}, false
	}
	token := d.acpToken()
	if token == "" {
		return ui.AcpState{}, false
	}
	return ui.AcpState{Base: fmt.Sprintf("ws://127.0.0.1:%d/acp/", port), Token: token, Agents: d.acpReadyAgents()}, true
}
