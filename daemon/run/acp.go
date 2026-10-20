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

// acpState is the daemon's side of it.
type acpState struct {
	home string // agentd's own home: where the token and the record are
	// port is the gateway's port, 0 until it listens and while it is off. The adapter store tells of a change on a
	// goroutine of its own, so the port is not read from d.gateway.
	port atomic.Int32

	mu       sync.Mutex
	last     acpauth.Info // the record last written, without its time
	problem  string       // the last problem with the token that was told
	recorded bool
}

// setupAcp makes the token, once, so that it is there before the first program asks. A daemon that cannot make it
// goes on: web pages do not use it, and no program gets in without it.
func (d *daemon) setupAcp() {
	d.acp0.home = home.AgentdHome(d.Host)
	if _, err := acpauth.Ensure(d.acp0.home); err != nil {
		d.tellAcpProblem(err)
	}
}

// acpToken is the token now, for the gateway to ask at each request: the file is the truth, so a new token (`agentd acp
// --rotate`) works at once. A file that cannot be trusted is replaced; one that cannot be made gives none, and then
// no program gets in.
func (d *daemon) acpToken() string {
	token, err := acpauth.Ensure(d.acp0.home)
	if err != nil {
		d.tellAcpProblem(err)
		return ""
	}
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

// writeAcpInfo records where the endpoint is and which agents are ready; it writes only when that changed. Without a
// gateway (every port taken) there is no endpoint, and no record.
func (d *daemon) writeAcpInfo() {
	port := int(d.acp0.port.Load())
	if port == 0 {
		return
	}
	info := acpauth.Info{Address: fmt.Sprintf("ws://127.0.0.1:%d/acp", port), Agents: d.acpReadyAgents()}
	d.acp0.mu.Lock()
	unchanged := d.acp0.recorded && d.acp0.last.Address == info.Address && slices.Equal(d.acp0.last.Agents, info.Agents)
	d.acp0.mu.Unlock()
	if unchanged {
		return
	}
	info.Updated = time.Now().UTC().Format(time.RFC3339)
	if err := acpauth.WriteInfo(d.acp0.home, info); err != nil {
		d.Log.Warn("cannot record where the standard ACP endpoint is: " + err.Error())
		return
	}
	d.acp0.mu.Lock()
	d.acp0.last, d.acp0.recorded = info, true
	d.acp0.mu.Unlock()
}

// removeAcpInfo takes the record away: the endpoint is gone with the daemon.
func (d *daemon) removeAcpInfo() {
	d.acp0.port.Store(0)
	d.acp0.mu.Lock()
	d.acp0.recorded = false
	d.acp0.mu.Unlock()
	if d.acp0.home != "" {
		_ = acpauth.RemoveInfo(d.acp0.home)
	}
}

// acpAccess is the address of the standard ACP endpoint with its token, for the app or the tray to copy for the user. It
// travels on the private channel the daemon shares with them and is never logged. False while there is no endpoint (every
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
