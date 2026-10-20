// Package pagetools is the daemon's side of a page's tools: the JSON messages between a page and the daemon on
// /v1/session, the handshake of the `mcp --page` bridge, and the Unix socket relay that carries the page's MCP lines
// to the agent's bridge and back.
package pagetools

import (
	"bytes"
	"encoding/json"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
)

// McpServer is a stdio MCP server as ACP's session/new takes it, without its name: the page names it.
type McpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []EnvVar `json:"env"`
}

// EnvVar is one environment variable of an McpServer.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ToDaemon kinds: what a page sends over /v1/session.
const (
	ToOpen  = "open"
	ToWrite = "write"
	ToClose = "close"
	ToMcp   = "mcp"
)

// ToDaemon is one message of a page. T picks the fields that are set: Agent for open, Line for write and mcp (a line
// of the page's own MCP server for the session's bridge). ID is never empty.
type ToDaemon struct {
	T     string
	ID    string
	Agent string
	Line  string
}

// ParseToDaemon reads a message of a page; false when it is not one of the four the daemon accepts.
func ParseToDaemon(text string) (ToDaemon, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return ToDaemon{}, false
	}
	id, ok := m["id"].(string)
	if !ok || id == "" {
		return ToDaemon{}, false
	}
	t, _ := m["t"].(string)
	switch t {
	case ToClose:
		return ToDaemon{T: ToClose, ID: id}, true
	case ToOpen:
		if agent, ok := m["agent"].(string); ok {
			return ToDaemon{T: ToOpen, ID: id, Agent: agent}, true
		}
	case ToWrite, ToMcp:
		if line, ok := m["line"].(string); ok {
			return ToDaemon{T: t, ID: id, Line: line}, true
		}
	}
	return ToDaemon{}, false
}

// FromDaemon is a message the daemon sends a page: Hello, Opened, Error, Frame or Mcp. Encode makes its wire form.
type FromDaemon interface {
	fromDaemon()
}

// AgentInfo is an agent's status as a page reads it in `hello` (the wire form of agents.AgentStatus).
type AgentInfo struct {
	Name string `json:"name"`
	// Title is the agent's name as a user reads it ("Claude Code"): apps show it as it is.
	Title  string `json:"title"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

// Hello is the first message of a socket: the daemon's version and which agents it can start.
type Hello struct {
	T       string      `json:"t"`
	Version string      `json:"version"`
	Agents  []AgentInfo `json:"agents"`
}

// Opened answers an `open`. Mcp is the stdio server through which the agent reaches the page's MCP server (nil when
// the daemon has no socket for it).
type Opened struct {
	T   string     `json:"t"`
	ID  string     `json:"id"`
	Pid int        `json:"pid"`
	Cwd string     `json:"cwd"`
	Mcp *McpServer `json:"mcp,omitempty"`
}

// Error is a refusal; ID is empty when no session is meant.
type Error struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	Message string `json:"message"`
}

// Frame carries the agent's lines as they are: a browser app is its own ACP client.
type Frame struct {
	T    string           `json:"t"`
	ID   string           `json:"id"`
	Kind agents.FrameKind `json:"kind"`
	Data string           `json:"data"`
	Code int              `json:"code"`
}

// Mcp carries a line of the agent's MCP client to the page's server.
type Mcp struct {
	T    string `json:"t"`
	ID   string `json:"id"`
	Line string `json:"line"`
}

func (Hello) fromDaemon()  {}
func (Opened) fromDaemon() {}
func (Error) fromDaemon()  {}
func (Frame) fromDaemon()  {}
func (Mcp) fromDaemon()    {}

// NewHello is the `hello` for a daemon of `version` whose agents are in `statuses`.
func NewHello(version string, statuses []agents.AgentStatus) Hello {
	infos := make([]AgentInfo, len(statuses))
	for i, s := range statuses {
		infos[i] = AgentInfo{Name: s.Name, Title: s.Title, Ready: s.Ready, Reason: s.Reason}
	}
	return Hello{T: "hello", Version: version, Agents: infos}
}

// NewOpened is the `opened` of session id; mcp is nil when the daemon has no bridge socket.
func NewOpened(id string, pid int, cwd string, mcp *McpServer) Opened {
	return Opened{T: "opened", ID: id, Pid: pid, Cwd: cwd, Mcp: mcp}
}

// NewError is an `error` for session id ("" when none is meant).
func NewError(id, message string) Error {
	return Error{T: "error", ID: id, Message: message}
}

// NewFrame is a `frame` of session id.
func NewFrame(id string, kind agents.FrameKind, data string, code int) Frame {
	return Frame{T: "frame", ID: id, Kind: kind, Data: data, Code: code}
}

// NewMcp is a line of the agent's MCP client for the page's server.
func NewMcp(id, line string) Mcp {
	return Mcp{T: "mcp", ID: id, Line: line}
}

// Encode is the JSON text of m, as JSON.stringify writes it: no HTML escaping of <, > and &.
func Encode(m FromDaemon) string {
	return stringify(m)
}

// stringify is the JSON text of v. v is made of strings, numbers, booleans and structs of them: it always encodes.
func stringify(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("pagetools: encoding a message: " + err.Error())
	}
	return string(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}
