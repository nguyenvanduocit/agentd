package goserver

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

// The values of Info.State.
const (
	// StateOnline is a paired daemon that is connected and speaks a protocol this app does: agents can be opened on it.
	StateOnline = "online"
	// StateOffline is every other paired daemon: not connected, or connected but incompatible (Info.Incompatible says
	// why in that case).
	StateOffline = "offline"
)

// MaxMessageBytes is the largest Socket.IO message the server accepts. The default of 1 MB ends the daemon's
// session on a large tool result; the ACP SDK allows 10 MiB per line. SocketOptions and Attach set it.
const MaxMessageBytes = 64 << 20

// ackTimeout bounds one call to the daemon that waits for an answer; `open` may have to fetch an adapter first.
const ackTimeout = 90 * time.Second

// Tools serves an app's MCP tools to the agents it starts: the agent reaches them through the bridge that BridgeCommand
// names, and the Server hands each bridge to Serve.
type Tools interface {
	// Owns reports whether grantID is a tool session the app opened for userID on daemonID. A bridge whose session
	// Owns refuses is turned away.
	Owns(userID, daemonID, grantID string) bool
	// Serve runs the MCP server of the session grantID of userID over t until ctx ends (the session or the bridge
	// ended). The agent's MCP client is at the other end of t.
	Serve(ctx context.Context, userID, grantID string, t mcp.Transport) error
}

// BridgeCommand is the stdio MCP server an app puts in the session/new of an agent so that the agent reaches the app's
// Tools for grantID: the daemon puts agentd-mcp on the PATH of every agent it starts, and that command carries the MCP
// lines to the app over a socket of its own, which Tools.Owns admits.
func BridgeCommand(grantID string) (command string, args []string) {
	return "agentd-mcp", []string{"--session", grantID}
}

// Options configure a Server. Only Tokens is required.
type Options struct {
	// Tokens is where paired daemons are kept. Required: New panics without it. MemoryTokens keeps them in memory,
	// which suits tests and examples; an app keeps them in its own database by implementing Tokens.
	Tokens Tokens
	// Tools serves the MCP tools of an agent session to the bridge the daemon starts for it. Nil: bridges are refused.
	Tools Tools
	// ClientIPHeader names the request header in which a trusted proxy puts the client's address; empty counts the
	// connection's own address. Set it only when every request passes through that proxy, which overwrites the header.
	ClientIPHeader string
	// Pairing is where the pairing codes and the count of wrong codes are kept. Nil keeps them in this process's memory
	// (NewMemoryPairingStore), which is right for one instance of an app. An app that runs several instances passes one
	// store they all share (Redis, SQL, ...), so that a code issued by one is redeemed at another and wrong codes count
	// together; the package pairingtest checks a store against the PairingStore contract. Since 0.8.0.
	Pairing PairingStore
	// MaxUnreadBytes bounds the agent output a session holds that the app has not read; a session past it ends. Zero is
	// 64 MiB, which still holds one message of MaxMessageBytes.
	MaxUnreadBytes int
	// Now is the clock (default time.Now); tests set it.
	Now func() time.Time
}

// peer is one connected daemon.
type peer struct {
	id, userID string
	name       string // the user's name for it; guarded by Server.mu
	raw        *socket.Socket
	client     *contract.Client
	// attached is closed once the socket is connected and client is set.
	attached chan struct{}
	// ready, info and seq are set by `hello`, under Server.mu: seq orders the user's daemons by when they came online.
	ready bool
	info  contract.Hello
	seq   uint64
	// ending says why the daemon is about to go (it was told to restart or stop): the sessions that die with it carry it.
	ending string
}

// Server holds the daemons that are connected, the sessions running on them, and the browser tabs watching a user's
// daemons. Pairing codes live in Options.Pairing.
type Server struct {
	opts Options

	mu       sync.Mutex
	peers    map[string]*peer                // by daemon id: the connected daemons
	refused  map[string]refused              // by daemon id: why its last hello was not accepted (protocol.go); outlives the connection
	bridges  map[string]map[*bridge]struct{} // by daemon id: the MCP bridges its token opened
	seq      uint64                          // the last peer.seq handed out
	sessions map[string]*session             // by session id
	// codeTimers publishes when a code this instance issued runs out. The pairing store holds the code.
	codeTimers map[string]*time.Timer
	watchers   map[string]map[*watcher]struct{}

	agentsChanged func(userID, daemonID string) // see OnAgentsChanged; guarded by mu

	pubMu sync.Mutex      // snapshots are computed and sent in one step, so a tab never ends on an older one
	sent  map[string]List // by user: the list their tabs hold; guarded by pubMu
}

// New is a Server. It panics when opts.Tokens is nil, so that a missing store shows when the app starts and not at the
// first request.
func New(opts Options) *Server {
	if opts.Tokens == nil {
		panic("goserver: Options.Tokens is required: pass goserver.NewMemoryTokens() to try it out, or your own Tokens store")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Pairing == nil {
		opts.Pairing = NewMemoryPairingStore()
	}
	if opts.MaxUnreadBytes <= 0 {
		opts.MaxUnreadBytes = defaultMaxUnreadBytes
	}
	return &Server{
		opts: opts, peers: map[string]*peer{}, refused: map[string]refused{}, bridges: map[string]map[*bridge]struct{}{}, sessions: map[string]*session{},
		codeTimers: map[string]*time.Timer{}, watchers: map[string]map[*watcher]struct{}{}, sent: map[string]List{},
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

// ErrBadName is what Rename answers for a name that is empty, longer than 64 characters, or holds a control or invisible
// formatting character. Test with errors.Is; the text says the rule. Since 0.7.0.
var ErrBadName = errors.New("a daemon's name is 1 to 64 characters, without control characters")

// maxNameRunes bounds a daemon's name.
const maxNameRunes = 64

// cleanName is name as the user may give it: trimmed, 1 to maxNameRunes characters, with no control or invisible
// formatting character (a right-to-left override could make one daemon pass for another in the list).
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > maxNameRunes || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return "", ErrBadName
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
func (h *Server) connected(userID, daemonID string) (*peer, error) {
	h.mu.Lock()
	p := h.peers[daemonID]
	ok := p != nil && p.userID == userID && p.ready
	why := h.refused[daemonID].reason
	h.mu.Unlock()
	if ok {
		return p, nil
	}
	name, known := h.Name(userID, daemonID)
	if !known {
		return nil, ErrUnknownDaemon
	}
	if why != "" {
		return nil, IncompatibleError{Name: name, Reason: why}
	}
	return nil, OfflineError{Name: name}
}

// Online lists the user's connected daemons that are ready for agents, the one that came online last first. It returns
// no error: a user without daemons gets an empty list.
func (h *Server) Online(userID string) []Ref {
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

// Name is the name of the user's paired daemon, online or not; false when the user has no daemon with that id.
func (h *Server) Name(userID, daemonID string) (string, bool) {
	rec, err := h.opts.Tokens.ByID(userID, daemonID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("daemon: look up daemon %s: %v", daemonID, err)
		}
		return "", false
	}
	return rec.Name, true
}

// Ready names the agents the user's daemon can start now ("claude", "codex"...).
//
// Errors: ErrUnknownDaemon when daemonID is not one of the user's daemons; OfflineError when it is paired but not
// connected; IncompatibleError when it connected but this app and it speak protocols that do not meet. Test with
// errors.Is and errors.As.
func (h *Server) Ready(userID, daemonID string) ([]string, error) {
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

// Open starts the named agent ("claude", "codex", "grok") on the user's daemon daemonID and returns the ACP stream of
// its process; the caller closes the Conn when done. ctx bounds the start, which may have to wait for the daemon to
// fetch the agent's adapter.
//
// Errors, all to test with errors.Is or errors.As:
//   - ErrUnknownDaemon: daemonID is not one of the user's daemons (it never existed, was disconnected, or is someone
//     else's). An empty daemonID is one too; OpenLatest picks a daemon for you.
//   - OfflineError: the daemon is paired but not connected.
//   - IncompatibleError: it is connected but this app and it speak protocols that do not meet.
//   - *AgentNotReadyError: the daemon does not have the agent, or says it is not ready.
//   - any other error: the daemon refused or did not answer; the text starts "your daemon:" and carries its message.
func (h *Server) Open(ctx context.Context, userID, daemonID, agentName string) (Conn, error) { //nolint:ireturn // Conn is the contract; the session is internal
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

// OpenLatest is Open on the user's daemon that came online last, for an app that does not ask the user to pick one. The
// errors of Open apply, with these in place of the ones about a chosen daemon: ErrNoDaemon when the user has no paired
// daemon (point them at pairing), and ErrNoOnlineDaemon when they have some and none is online (ask them to start one).
//
// Since 0.7.0.
func (h *Server) OpenLatest(ctx context.Context, userID, agentName string) (Conn, error) { //nolint:ireturn // Conn is the contract; the session is internal
	online := h.Online(userID)
	if len(online) == 0 {
		recs, err := h.opts.Tokens.List(userID)
		if err != nil {
			return nil, fmt.Errorf("list daemons: %w", err)
		}
		if len(recs) == 0 {
			return nil, ErrNoDaemon
		}
		return nil, ErrNoOnlineDaemon
	}
	return h.Open(ctx, userID, online[0].ID, agentName)
}

// checkReady refuses an agent the daemon said it cannot start, naming the agent and the daemon and giving the reason
// the daemon gave. It reads what the daemon reported, so nothing is started to find out.
func (h *Server) checkReady(agents []contract.AgentInfo, daemonName, agentName string) error {
	i := slices.IndexFunc(agents, func(a contract.AgentInfo) bool { return a.Name == agentName })
	switch {
	case i < 0:
		return &AgentNotReadyError{Daemon: daemonName, Agent: agentName, Title: agentName}
	case !agents[i].Ready:
		return &AgentNotReadyError{Daemon: daemonName, Agent: agentName, Title: title(agents[i]), Reason: agents[i].Reason, Offered: true}
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
func (h *Server) forget(sessionID string) {
	h.mu.Lock()
	s := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if s != nil && s.opened.Load() {
		h.publish(s.p.userID)
	}
}

// session returns the session sessionID if it runs on p.
func (h *Server) session(p *peer, sessionID string) *session {
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
func (h *Server) stillPaired(rec Record) error {
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
func (h *Server) connect(p *peer, rec Record) error {
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

// hello records what the daemon reported about itself and tells the user's tabs it is online. A daemon whose protocols
// do not include this app's is not online: the list says why, and Open fails with an IncompatibleError.
func (h *Server) hello(p *peer, info contract.Hello) {
	reason := incompatibility(Protocol, info.ProtocolMin, info.ProtocolMax)
	h.mu.Lock()
	if h.peers[p.id] != p {
		h.mu.Unlock()
		return
	}
	if reason != "" {
		p.ready = false
		h.refused[p.id] = refused{reason: reason, version: info.Version, hostname: info.Hostname, os: info.Os}
		h.mu.Unlock()
		h.touch(p.id)
		h.publish(p.userID)
		return
	}
	delete(h.refused, p.id)
	h.seq++
	p.info, p.ready, p.seq = info, true, h.seq
	h.mu.Unlock()
	h.touch(p.id)
	h.publish(p.userID)
	h.notifyAgents(p) // a daemon that reconnects may be a new process with other agents
}

// OnAgentsChanged registers fn, called with the user and the daemon whenever what a daemon can run may have changed:
// it said hello, or reported a new list of agents. An app uses it to drop what it cached about that daemon. A Server
// has one such function: registering another replaces the first, and nil removes it.
func (h *Server) OnAgentsChanged(fn func(userID, daemonID string)) {
	h.mu.Lock()
	h.agentsChanged = fn
	h.mu.Unlock()
}

func (h *Server) notifyAgents(p *peer) {
	h.mu.Lock()
	fn := h.agentsChanged
	h.mu.Unlock()
	if fn != nil {
		fn(p.userID, p.id)
	}
}

// agents records the list of agents p reported after its hello. Only the daemon's current connection may do it: the
// daemon is the one of the authenticated socket, never one named in the call.
func (h *Server) agents(p *peer, list []contract.AgentInfo) {
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
func (h *Server) touch(daemonID string) {
	if err := h.opts.Tokens.Seen(daemonID, h.opts.Now().UnixMilli()); err != nil {
		log.Printf("daemon: record last seen of %s: %v", daemonID, err)
	}
}

// disconnected ends every session on p, as a socket that dropped leaves the agents unreachable, and tells the user's
// tabs when p was its daemon's current connection.
func (h *Server) disconnected(p *peer) {
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
func (h *Server) addBridge(rec Record, b *bridge) error {
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

func (h *Server) removeBridge(b *bridge) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.bridges[b.daemonID], b)
	if len(h.bridges[b.daemonID]) == 0 {
		delete(h.bridges, b.daemonID)
	}
}

// Revoke disconnects the user's daemon id: its record is removed, so its token stops working, and its connection
// and the MCP bridges of its token are closed at once. It returns the user's list afterwards.
//
// Errors: ErrUnknownDaemon when id is not one of the user's daemons; otherwise the error of the Tokens store, wrapped.
func (h *Server) Revoke(userID, id string) (List, error) {
	if err := h.opts.Tokens.Delete(userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return List{}, ErrUnknownDaemon
		}
		return List{}, fmt.Errorf("disconnect the daemon: %w", err)
	}
	// Tokens is the app's code: a Delete that forgot the user condition must not let one user cut off another's
	// daemon, so only the sockets of userID are closed.
	h.mu.Lock()
	p := h.peers[id]
	if p != nil && p.userID == userID {
		delete(h.peers, id) // offline from this moment, before its socket has finished closing
	} else {
		p = nil
	}
	delete(h.refused, id) // the token is gone: Delete above did not find another user's daemon, or it would have failed
	var bridges []*bridge
	for b := range h.bridges[id] {
		if b.userID == userID {
			bridges = append(bridges, b)
			delete(h.bridges[id], b)
		}
	}
	if len(h.bridges[id]) == 0 {
		delete(h.bridges, id)
	}
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

// Rename gives the user's daemon id a new name and returns the user's list afterwards.
//
// Errors: ErrBadName when name breaks the rule (1 to 64 characters, no control characters); ErrUnknownDaemon when id is
// not one of the user's daemons; ErrNameTaken when another daemon of the user has the name.
func (h *Server) Rename(userID, id, name string) (List, error) {
	name, err := cleanName(name)
	if err != nil {
		return List{}, err
	}
	if _, err := h.opts.Tokens.Rename(userID, id, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return List{}, ErrUnknownDaemon
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

// title is an agent's name as the user reads it, as its daemon reported it; a daemon older than 0.3 reports none.
func title(a contract.AgentInfo) string { return cmp.Or(a.Title, a.Name) }

// agentsOf is what the daemon reported, in its order.
func agentsOf(reported []contract.AgentInfo) []Agent {
	out := make([]Agent, len(reported))
	for i, a := range reported {
		out[i] = Agent{Name: a.Name, Title: title(a), Ready: a.Ready, Reason: a.Reason}
	}
	return out
}

// List is the user's daemons as the browser shows them, with the pending pairing code, if any. The only error is the
// Tokens store failing. A pairing store that cannot say which code is pending leaves the code off the list.
func (h *Server) List(userID string) (List, error) {
	recs, err := h.opts.Tokens.List(userID)
	if err != nil {
		return List{}, fmt.Errorf("list daemons: %w", err)
	}
	h.mu.Lock()
	sessions := map[*peer]int{} // the agent sessions each daemon runs
	for _, s := range h.sessions {
		if s.opened.Load() {
			sessions[s.p]++
		}
	}
	list := List{Daemons: make([]Info, 0, len(recs))}
	for _, rec := range recs {
		info := Info{
			ID: rec.ID, Name: rec.Name, Hostname: rec.Hostname, State: StateOffline, OS: rec.OS, Agents: []Agent{},
			LastSeen: rec.LastSeen,
		}
		if p := h.peers[rec.ID]; p != nil && p.ready {
			info.State, info.Hostname, info.Version, info.OS = StateOnline, p.info.Hostname, p.info.Version, p.info.Os
			info.Agents, info.Managed, info.Sessions = agentsOf(p.info.Agents), p.info.Managed, sessions[p]
		} else if r, ok := h.refused[rec.ID]; ok {
			info.Incompatible, info.Hostname, info.Version, info.OS = r.reason, r.hostname, r.version, r.os
		}
		list.Daemons = append(list.Daemons, info)
	}
	h.mu.Unlock()
	code, expires, err := h.opts.Pairing.Pending(context.Background(), userID, h.opts.Now())
	if err == nil && code != "" {
		list.Pairing = Pairing{Code: displayCode(code), ExpiresAt: expires.UnixMilli()}
	}
	return list, nil
}

// UserDaemons is the Server as one user sees their own daemons: an app hands it to its UI layer, so that no call can
// name another user. Every method is the Server's with the user filled in, and answers the same errors.
type UserDaemons struct {
	server *Server
	userID string
}

// For is the Server seen from userID's side.
func (h *Server) For(userID string) UserDaemons { return UserDaemons{server: h, userID: userID} }

// List is Server.List for this user.
func (u UserDaemons) List() (List, error) { return u.server.List(u.userID) }

// StartPairing is Server.StartPairing for this user: a new code, replacing the one they had.
func (u UserDaemons) StartPairing() (List, error) { return u.server.StartPairing(u.userID) }

// CancelPairing is Server.CancelPairing for this user.
func (u UserDaemons) CancelPairing() (List, error) { return u.server.CancelPairing(u.userID) }

// Rename is Server.Rename for this user.
func (u UserDaemons) Rename(id, name string) (List, error) {
	return u.server.Rename(u.userID, id, name)
}

// Revoke is Server.Revoke for this user.
func (u UserDaemons) Revoke(id string) (List, error) { return u.server.Revoke(u.userID, id) }

// Restart is Server.Restart for this user.
func (u UserDaemons) Restart(id string) (List, error) { return u.server.Restart(u.userID, id) }

// Stop is Server.Stop for this user.
func (u UserDaemons) Stop(id string) (List, error) { return u.server.Stop(u.userID, id) }
