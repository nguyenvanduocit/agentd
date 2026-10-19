// Package hub connects an app's server to the agentd daemons on users' machines. A daemon dials out over Socket.IO
// (namespace /agentd), proves whose it is with a token it got by pairing, and then starts agents on the server's
// behalf and relays their ACP streams. A user pairs any number of daemons. The hub makes each of them look like a
// place agents can be opened, and tells the user's browser tabs which are there.
package hub

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/contract"
)

// Daemon states of Info.
const (
	stateOnline  = "online"
	stateOffline = "offline"
)

// MaxMessageBytes is the largest Socket.IO message the server accepts. The default of 1 MB ends the daemon's
// session on a large tool result; the ACP SDK allows 10 MiB per line.
const MaxMessageBytes = 64 << 20

// ackTimeout bounds one call to the daemon that waits for an answer; `open` may have to fetch an adapter first.
const ackTimeout = 90 * time.Second

// Tools serves the graph tools of one agent session to a bridge.
type Tools interface {
	// Owns reports whether grantID was opened by userID on daemonID.
	Owns(userID, daemonID, grantID string) bool
	Serve(ctx context.Context, userID, grantID string, t mcp.Transport) error
}

type Options struct {
	Tokens Tokens
	// Tools serves the MCP tools of an agent session to the bridge the daemon starts for it. Nil: bridges are refused.
	Tools Tools
	// AgentOrder lists the agents the app knows, in the order its UI shows them; agents a daemon reports that are not
	// listed follow, in the order reported.
	AgentOrder []string
	// AgentTitle is the name of an agent as the user reads it ("claude" is "Claude"); nil keeps the name.
	AgentTitle func(name string) string
	// LatestVersion is the newest daemon release the app knows of, for List.LatestVersion; nil says none.
	LatestVersion func() string
	// ClientIPHeader names the request header in which a trusted proxy puts the client's address; empty counts the
	// connection's own address. Set it only when every request passes through that proxy, which overwrites the header.
	ClientIPHeader string
	// Now is the clock (default time.Now); tests set it.
	Now func() time.Time
}

// peer is one connected daemon.
type peer struct {
	id, userID string
	name       string // the user's name for it; guarded by Hub.mu
	raw        *socket.Socket
	client     *contract.Client
	// attached is closed once the socket is connected and client is set.
	attached chan struct{}
	// ready, info and seq are set by `hello`, under Hub.mu: seq orders the user's daemons by when they came online.
	ready bool
	info  contract.Hello
	seq   uint64
	// ending says why the daemon is about to go (it was told to restart or stop): the sessions that die with it carry it.
	ending string
}

// Hub holds the daemons that are connected, the sessions running on them, the pairing codes and the browser tabs
// watching a user's daemons.
type Hub struct {
	opts Options

	mu       sync.Mutex
	peers    map[string]*peer                // by daemon id: the connected daemons
	bridges  map[string]map[*bridge]struct{} // by daemon id: the MCP bridges its token opened
	seq      uint64                          // the last peer.seq handed out
	sessions map[string]*session             // by session id
	codes    map[string]*pairCode
	failures map[string][]time.Time // recent wrong pairing codes per client address (pair.go clientIP)
	// failuresSwept is when failures last dropped its aged-out clients.
	failuresSwept time.Time
	watchers      map[string]map[*watcher]struct{}

	agentsChanged func(userID, daemonID string) // see OnAgentsChanged; guarded by mu

	pubMu sync.Mutex      // snapshots are computed and sent in one step, so a tab never ends on an older one
	sent  map[string]List // by user: the list their tabs hold; guarded by pubMu
}

func New(opts Options) *Hub {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Hub{
		opts: opts, peers: map[string]*peer{}, bridges: map[string]map[*bridge]struct{}{}, sessions: map[string]*session{},
		codes: map[string]*pairCode{}, failures: map[string][]time.Time{}, watchers: map[string]map[*watcher]struct{}{}, sent: map[string]List{},
	}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source failing is not recoverable
	}
	return hex.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Errors the browser sees.
var (
	// ErrNoSuchDaemon is the answer for a daemon id the user does not have, whether it never existed, was
	// disconnected, or belongs to someone else.
	ErrNoSuchDaemon = errors.New("no such daemon")
	errBadName      = errors.New("a daemon's name is 1 to 64 characters, without control characters")
)

// maxNameRunes bounds a daemon's name.
const maxNameRunes = 64

// cleanName is name as the user may give it: trimmed, 1 to maxNameRunes characters, with no control or invisible
// formatting character (a right-to-left override could make one daemon pass for another in the list).
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > maxNameRunes || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return "", errBadName
	}
	return name, nil
}

// defaultName is the name a daemon gets when it pairs: its machine's hostname, made fit, or "Daemon" when that
// leaves nothing.
func defaultName(hostname string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, hostname)
	if r := []rune(strings.TrimSpace(clean)); len(r) > maxNameRunes {
		clean = string(r[:maxNameRunes])
	}
	if name, err := cleanName(clean); err == nil {
		return name
	}
	return "Daemon"
}

// connected is the user's daemon id once it said hello. When it is not online the error says why: it is paired and
// offline (OfflineError), or it is not the user's (ErrUnknownDaemon).
func (h *Hub) connected(userID, daemonID string) (*peer, error) {
	h.mu.Lock()
	p := h.peers[daemonID]
	ok := p != nil && p.userID == userID && p.ready
	h.mu.Unlock()
	if ok {
		return p, nil
	}
	name, known := h.Name(userID, daemonID)
	if !known {
		return nil, ErrUnknownDaemon
	}
	return nil, OfflineError{Name: name}
}

// Online lists the user's connected daemons, the one that came online last first.
func (h *Hub) Online(userID string) []Ref {
	h.mu.Lock()
	var ps []*peer
	for _, p := range h.peers {
		if p.userID == userID && p.ready {
			ps = append(ps, p)
		}
	}
	slices.SortFunc(ps, func(a, b *peer) int { return cmp.Compare(b.seq, a.seq) })
	out := make([]Ref, len(ps))
	for i, p := range ps {
		out[i] = Ref{ID: p.id, Name: p.name}
	}
	h.mu.Unlock()
	return out
}

// Name is the name of the user's paired daemon, online or not.
func (h *Hub) Name(userID, daemonID string) (string, bool) {
	rec, err := h.opts.Tokens.ByID(userID, daemonID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("daemon: look up daemon %s: %v", daemonID, err)
		}
		return "", false
	}
	return rec.Name, true
}

// Ready names the agents the user's daemon can start now.
func (h *Hub) Ready(userID, daemonID string) ([]string, error) {
	p, err := h.connected(userID, daemonID)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var names []string
	for _, a := range p.info.Agents {
		if a.Ready {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

// Open starts the named agent on the user's daemon.
func (h *Hub) Open(ctx context.Context, userID, daemonID, agentName string) (Conn, error) { //nolint:ireturn // implements agent.Daemon
	p, err := h.connected(userID, daemonID)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	agents, name := p.info.Agents, p.name
	h.mu.Unlock()
	if cerr := h.checkReady(agents, name, agentName); cerr != nil {
		return nil, cerr
	}
	s := newSession(h, newID(), p)
	h.mu.Lock()
	h.sessions[s.id] = s // before the call: lines can arrive before the answer
	h.mu.Unlock()
	opened, err := p.client.CallOpen(ctx, s.id, agentName)
	if err != nil {
		h.forget(s.id)
		return nil, daemonError(err)
	}
	s.setCwd(opened.Cwd)
	h.publish(p.userID) // the daemon runs one more session
	return s, nil
}

// checkReady refuses an agent the daemon said it cannot start, naming the agent and the daemon and giving the reason
// the daemon gave. It reads what the daemon reported, so nothing is started to find out.
func (h *Hub) checkReady(agents []contract.AgentInfo, daemonName, agentName string) error {
	i := slices.IndexFunc(agents, func(a contract.AgentInfo) bool { return a.Name == agentName })
	switch {
	case i < 0:
		return fmt.Errorf("%s does not offer %s", daemonName, h.title(agentName))
	case !agents[i].Ready && agents[i].Reason != "":
		return fmt.Errorf("%s is not ready on %s: %s", h.title(agentName), daemonName, agents[i].Reason)
	case !agents[i].Ready:
		return fmt.Errorf("%s is not ready on %s", h.title(agentName), daemonName)
	}
	return nil
}

// daemonError is err with the daemon's own message when the daemon answered with an error.
func daemonError(err error) error {
	var re *contract.RpcError
	if errors.As(err, &re) && re.Message != "" {
		return fmt.Errorf("your daemon: %s", re.Message)
	}
	return fmt.Errorf("your daemon: %w", err)
}

// forget drops a session and, when the daemon had it open, tells the user's tabs that it runs one less.
func (h *Hub) forget(sessionID string) {
	h.mu.Lock()
	s := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if s != nil && s.opened.Load() {
		h.publish(s.p.userID)
	}
}

// session returns the session sessionID if it runs on p.
func (h *Hub) session(p *peer, sessionID string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[sessionID]; s != nil && s.p == p {
		return s
	}
	return nil
}

// errRevoked means the daemon's token was removed while it was connecting.
var errRevoked = errors.New("the daemon was disconnected by its owner")

// stillPaired checks that the daemon of rec has not been revoked. A connection registers itself first and asks
// afterwards, and Revoke deletes the record first and collects the connections afterwards: whichever order the two
// interleave in, a connection that registered before the record went is seen by Revoke, and one that registered
// after finds the record gone here. A revoked daemon never stays connected.
func (h *Hub) stillPaired(rec Record) error {
	if _, err := h.opts.Tokens.ByID(rec.UserID, rec.ID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return errRevoked
		}
		return fmt.Errorf("look up the daemon: %w", err)
	}
	return nil
}

// connect makes p the connection of its daemon. A connection the daemon had before is disconnected: one connection
// per daemon.
func (h *Hub) connect(p *peer, rec Record) error {
	h.mu.Lock()
	old := h.peers[p.id]
	h.peers[p.id] = p
	h.mu.Unlock()
	if old != nil {
		old.raw.Disconnect(true)
	}
	if err := h.stillPaired(rec); err != nil {
		h.mu.Lock()
		if h.peers[p.id] == p {
			delete(h.peers, p.id)
		}
		h.mu.Unlock()
		return err
	}
	return nil
}

// hello records what the daemon reported about itself and tells the user's tabs it is online.
func (h *Hub) hello(p *peer, info contract.Hello) {
	h.mu.Lock()
	if h.peers[p.id] != p {
		h.mu.Unlock()
		return
	}
	h.seq++
	p.info, p.ready, p.seq = info, true, h.seq
	h.mu.Unlock()
	h.touch(p.id)
	h.publish(p.userID)
	h.notifyAgents(p) // a daemon that reconnects may be a new process with other agents
}

// OnAgentsChanged registers fn, called with the user and the daemon whenever what a daemon can run may have changed:
// it said hello, or reported a new list of agents. The chat runtime uses it to drop what it cached about that daemon.
func (h *Hub) OnAgentsChanged(fn func(userID, daemonID string)) {
	h.mu.Lock()
	h.agentsChanged = fn
	h.mu.Unlock()
}

func (h *Hub) notifyAgents(p *peer) {
	h.mu.Lock()
	fn := h.agentsChanged
	h.mu.Unlock()
	if fn != nil {
		fn(p.userID, p.id)
	}
}

// agents records the list of agents p reported after its hello. Only the daemon's current connection may do it: the
// daemon is the one of the authenticated socket, never one named in the call.
func (h *Hub) agents(p *peer, list []contract.AgentInfo) {
	h.mu.Lock()
	if h.peers[p.id] != p || !p.ready || slices.Equal(p.info.Agents, list) {
		h.mu.Unlock()
		return
	}
	p.info.Agents = slices.Clone(list)
	h.mu.Unlock()
	h.notifyAgents(p)
	h.publish(p.userID)
}

// touch records that the daemon was connected just now.
func (h *Hub) touch(daemonID string) {
	if err := h.opts.Tokens.Seen(daemonID, h.opts.Now().UnixMilli()); err != nil {
		log.Printf("daemon: record last seen of %s: %v", daemonID, err)
	}
}

// disconnected ends every session on p, as a socket that dropped leaves the agents unreachable, and tells the user's
// tabs when p was its daemon's current connection.
func (h *Hub) disconnected(p *peer) {
	h.mu.Lock()
	current := h.peers[p.id] == p
	if current {
		delete(h.peers, p.id)
	}
	var mine []*session
	for _, s := range h.sessions {
		if s.p == p {
			mine = append(mine, s)
		}
	}
	h.mu.Unlock()
	for _, s := range mine {
		s.finish(h.noteOf(p, "the daemon disconnected"))
	}
	if current {
		h.touch(p.id)
		h.publish(p.userID)
	}
}

// addBridge registers a bridge of the daemon, so that revoking the daemon cuts it off. The record is checked
// afterwards, like a daemon's own connection.
func (h *Hub) addBridge(rec Record, b *bridge) error {
	h.mu.Lock()
	if h.bridges[rec.ID] == nil {
		h.bridges[rec.ID] = map[*bridge]struct{}{}
	}
	h.bridges[rec.ID][b] = struct{}{}
	h.mu.Unlock()
	if err := h.stillPaired(rec); err != nil {
		h.removeBridge(b)
		return err
	}
	return nil
}

func (h *Hub) removeBridge(b *bridge) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.bridges[b.daemonID], b)
	if len(h.bridges[b.daemonID]) == 0 {
		delete(h.bridges, b.daemonID)
	}
}

// Revoke disconnects the user's daemon id: its record is removed, so its token stops working, and its connection
// and the MCP bridges of its token are closed at once.
func (h *Hub) Revoke(userID, id string) (List, error) {
	if err := h.opts.Tokens.Delete(userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return List{}, ErrNoSuchDaemon
		}
		return List{}, fmt.Errorf("disconnect the daemon: %w", err)
	}
	h.mu.Lock()
	p := h.peers[id]
	delete(h.peers, id) // offline from this moment, before its socket has finished closing
	var bridges []*bridge
	for b := range h.bridges[id] {
		bridges = append(bridges, b)
	}
	delete(h.bridges, id)
	h.mu.Unlock()
	if p != nil {
		p.raw.Disconnect(true)
	}
	for _, b := range bridges {
		b.raw.Disconnect(true)
	}
	h.publish(userID)
	return h.List(userID)
}

// Rename gives the user's daemon id a new name.
func (h *Hub) Rename(userID, id, name string) (List, error) {
	name, err := cleanName(name)
	if err != nil {
		return List{}, err
	}
	if _, err := h.opts.Tokens.Rename(userID, id, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return List{}, ErrNoSuchDaemon
		}
		return List{}, err
	}
	h.mu.Lock()
	if p := h.peers[id]; p != nil && p.userID == userID {
		p.name = name
	}
	h.mu.Unlock()
	h.publish(userID)
	return h.List(userID)
}

// title is an agent's name as the user reads it.
func (h *Hub) title(name string) string {
	if h.opts.AgentTitle == nil {
		return name
	}
	return h.opts.AgentTitle(name)
}

// orderedAgents lists what the daemon reported, the agents the app knows first in their fixed order.
func (h *Hub) orderedAgents(reported []contract.AgentInfo) []Agent {
	out := make([]Agent, 0, len(reported))
	add := func(a contract.AgentInfo) {
		out = append(out, Agent{Name: a.Name, Title: h.title(a.Name), Ready: a.Ready, Reason: a.Reason})
	}
	for _, name := range h.opts.AgentOrder {
		if i := slices.IndexFunc(reported, func(a contract.AgentInfo) bool { return a.Name == name }); i >= 0 {
			add(reported[i])
		}
	}
	for _, a := range reported {
		if !slices.Contains(h.opts.AgentOrder, a.Name) {
			add(a)
		}
	}
	return out
}

// latestVersion is the newest daemon release the app says it knows of.
func (h *Hub) latestVersion() string {
	if h.opts.LatestVersion == nil {
		return ""
	}
	return h.opts.LatestVersion()
}

// List is the user's daemons as the browser shows them, with the pending pairing code, if any.
func (h *Hub) List(userID string) (List, error) {
	recs, err := h.opts.Tokens.List(userID)
	if err != nil {
		return List{}, fmt.Errorf("list daemons: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sessions := map[*peer]int{} // the agent sessions each daemon runs
	for _, s := range h.sessions {
		if s.opened.Load() {
			sessions[s.p]++
		}
	}
	list := List{Daemons: make([]Info, 0, len(recs)), LatestVersion: h.latestVersion()}
	for _, rec := range recs {
		info := Info{
			ID: rec.ID, Name: rec.Name, Hostname: rec.Hostname, State: stateOffline, OS: rec.OS, Agents: []Agent{},
			LastSeen: rec.LastSeen,
		}
		if p := h.peers[rec.ID]; p != nil && p.ready {
			info.State, info.Hostname, info.Version, info.OS = stateOnline, p.info.Hostname, p.info.Version, p.info.Os
			info.Agents, info.Managed, info.Sessions = h.orderedAgents(p.info.Agents), p.info.Managed, sessions[p]
		}
		list.Daemons = append(list.Daemons, info)
	}
	if code := h.liveCodeLocked(userID); code != nil {
		list.Pairing = Pairing{Code: code.text, ExpiresAt: code.expires.UnixMilli()}
	}
	return list, nil
}

// UserDaemons is the hub as one user sees their own daemons: an app hands it to its UI layer.
type UserDaemons struct {
	hub    *Hub
	userID string
}

// For is the hub seen from userID's side.
func (h *Hub) For(userID string) UserDaemons { return UserDaemons{hub: h, userID: userID} }

func (u UserDaemons) List() (List, error)          { return u.hub.List(u.userID) }
func (u UserDaemons) StartPairing() (List, error)  { return u.hub.StartPairing(u.userID) }
func (u UserDaemons) CancelPairing() (List, error) { return u.hub.CancelPairing(u.userID) }

func (u UserDaemons) Rename(id, name string) (List, error) {
	return u.hub.Rename(u.userID, id, name)
}

func (u UserDaemons) Revoke(id string) (List, error) { return u.hub.Revoke(u.userID, id) }

func (u UserDaemons) Restart(id string) (List, error) { return u.hub.Restart(u.userID, id) }
func (u UserDaemons) Stop(id string) (List, error)    { return u.hub.Stop(u.userID, id) }
