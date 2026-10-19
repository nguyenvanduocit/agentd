// Package gateway is the HTTP and WebSocket server on 127.0.0.1 that web pages use to ask for access and to run
// agents: the rules of its requests (this file, pure), the server (gateway.go) and the sockets of pages that were
// allowed to run agents (sessions.go).
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// DefaultPort is the first port the gateway tries.
const DefaultPort = 47615

// PortRange is how many ports the gateway tries, from the first one.
const PortRange = 10

// PortVariable names the first port; 0 means any free port (tests).
const PortVariable = "AGENTD_LOCAL_PORT"

// GatewayPort is the first port the gateway tries: $AGENTD_LOCAL_PORT (0 = any free port, for tests), else 47615.
func GatewayPort(env home.Env) (int, error) {
	raw := env[PortVariable]
	if raw == "" {
		return DefaultPort, nil
	}
	last := 65535 - PortRange + 1
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 0 || port > last {
		return 0, fmt.Errorf("%s \"%s\" is not a port from 0 to %d", PortVariable, raw, last)
	}
	return port, nil
}

// HostAllowed is whether the Host header of a request to the gateway is a loopback name and the gateway's own port.
// Anything else is DNS rebinding.
func HostAllowed(host string, port int) bool {
	h := strings.ToLower(host)
	p := strconv.Itoa(port)
	return h == "127.0.0.1:"+p || h == "localhost:"+p
}

// defaultPorts are the ports a URL leaves out of its host, by scheme.
var defaultPorts = map[string]string{"http": "80", "https": "443", "ws": "80", "wss": "443"}

// OriginOf is the page's origin from its Origin header; false when it is missing, `null`, has a path or anything
// besides scheme, host and port, or may not be trusted (plain http is accepted for a loopback host only, as for
// server addresses).
func OriginOf(header string) (string, bool) {
	if header == "" || header == "null" {
		return "", false
	}
	u, err := url.Parse(header)
	if err != nil || u.Host == "" {
		return "", false
	}
	def, known := defaultPorts[u.Scheme]
	if !known {
		return "", false
	}
	if strings.ContainsFunc(header, func(r rune) bool { return r > unicode.MaxASCII }) {
		return "", false
	}
	// The header must be the origin in its serialized form, byte for byte: a browser sends nothing else.
	canonical := u.Scheme + "://" + strings.ToLower(u.Hostname())
	if strings.Contains(u.Hostname(), ":") {
		canonical = u.Scheme + "://[" + strings.ToLower(u.Hostname()) + "]"
	}
	if port := u.Port(); port != "" && port != def {
		n, err := strconv.Atoi(port)
		if err != nil || n > 65535 || strconv.Itoa(n) != port {
			return "", false
		}
		canonical += ":" + port
	}
	if canonical != header {
		return "", false
	}
	origin, err := home.ServerOrigin(header)
	if err != nil {
		return "", false
	}
	return origin, true
}

// CORSHeaders are the CORS headers for one caller. The origin is reflected, never "*": the answer differs per origin.
func CORSHeaders(origin string, request http.Header) map[string]string {
	headers := map[string]string{
		"Access-Control-Allow-Origin":  origin,
		"Vary":                         "Origin",
		"Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
		"Access-Control-Allow-Headers": "content-type",
	}
	if request.Get("Access-Control-Request-Private-Network") == "true" {
		headers["Access-Control-Allow-Private-Network"] = "true"
	}
	return headers
}

// ConnectRequest is the body of POST /v1/connect: Code is set for ModeServer only.
type ConnectRequest struct {
	Mode consent.Mode
	Code string
}

// ParseConnect reads the body of POST /v1/connect; false when it is malformed. The app a page connects is the one at
// its own origin: a body that names a server is refused, so a page cannot point the daemon at someone else's server.
func ParseConnect(body []byte) (ConnectRequest, bool) {
	var b map[string]any
	if json.Unmarshal(body, &b) != nil || b == nil {
		return ConnectRequest{}, false
	}
	if b["mode"] == "browser" {
		return ConnectRequest{Mode: consent.ModeBrowser}, true
	}
	code, isText := b["code"].(string)
	_, namesServer := b["server"]
	if b["mode"] != "server" || !isText || code == "" || namesServer {
		return ConnectRequest{}, false
	}
	return ConnectRequest{Mode: consent.ModeServer, Code: code}, true
}

// Cooldown: origins that said no (or did not answer) wait before the dialog can open for them again. It is not safe
// for concurrent use; its caller serializes.
type Cooldown struct {
	d     time.Duration
	now   func() time.Time
	until map[string]time.Time
}

// NewCooldown is a cooldown of d per origin on the clock now.
func NewCooldown(d time.Duration, now func() time.Time) *Cooldown {
	return &Cooldown{d: d, now: now, until: map[string]time.Time{}}
}

// Block starts origin's wait.
func (c *Cooldown) Block(origin string) {
	c.until[origin] = c.now().Add(c.d)
}

// Blocked is whether origin still waits.
func (c *Cooldown) Blocked(origin string) bool {
	return c.until[origin].After(c.now())
}

// Lockout: refusals count across every origin, because a page that asks from a new subdomain after each Deny would
// otherwise open a dialog each time. `limit` refusals within `window` lock every dialog until the oldest of them is out
// of the window. It is not safe for concurrent use; its caller serializes.
type Lockout struct {
	limit   int
	window  time.Duration
	now     func() time.Time
	strikes []time.Time
}

// NewLockout is a lockout after limit refusals within window, on the clock now.
func NewLockout(limit int, window time.Duration, now func() time.Time) *Lockout {
	return &Lockout{limit: limit, window: window, now: now}
}

// Strike counts a refusal.
func (l *Lockout) Strike() {
	l.strikes = append(l.recent(), l.now())
}

// Locked is whether every dialog is locked now.
func (l *Lockout) Locked() bool {
	return len(l.recent()) >= l.limit
}

// Reset is an Allow: the user is paying attention, what came before no longer counts.
func (l *Lockout) Reset() {
	l.strikes = nil
}

func (l *Lockout) recent() []time.Time {
	now := l.now()
	var kept []time.Time
	for _, t := range l.strikes {
		if t.Add(l.window).After(now) {
			kept = append(kept, t)
		}
	}
	return kept
}
