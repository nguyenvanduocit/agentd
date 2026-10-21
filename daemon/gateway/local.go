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

// maxNameRunes is the longest name an app can give itself; a longer one is cut.
const maxNameRunes = 64

// CleanName is the name an app gave itself as the window and the menu show it: the app chooses it, so characters that
// hide or reorder text (controls, and format characters such as the right-to-left override and the zero-width space)
// are dropped, runs of white space become one space, and a long name is cut to maxNameRunes. Empty when nothing is left.
func CleanName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	words := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(words); len(runes) > maxNameRunes {
		return strings.TrimSpace(string(runes[:maxNameRunes]))
	}
	return words
}

// ConnectRequest is the body of POST /v1/connect: Name is what the app calls itself; Code is set for ModeServer only.
type ConnectRequest struct {
	Mode consent.Mode
	Name string
	Code string
}

// ParseConnect reads the body of POST /v1/connect; false when it is malformed. The app a page connects is the one at
// its own origin: a body that names a server is refused, so a page cannot point the daemon at someone else's server.
// Every request says who the app is (name): one that does not, or whose name is nothing once cleaned, is malformed.
func ParseConnect(body []byte) (ConnectRequest, bool) {
	var b map[string]any
	if json.Unmarshal(body, &b) != nil || b == nil {
		return ConnectRequest{}, false
	}
	text, isName := b["name"].(string)
	name := CleanName(text)
	if !isName || name == "" {
		return ConnectRequest{}, false
	}
	if b["mode"] == "browser" {
		return ConnectRequest{Mode: consent.ModeBrowser, Name: name}, true
	}
	code, isText := b["code"].(string)
	_, namesServer := b["server"]
	if b["mode"] != "server" || !isText || code == "" || namesServer {
		return ConnectRequest{}, false
	}
	return ConnectRequest{Mode: consent.ModeServer, Name: name, Code: code}, true
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

// Wait is how long origin still waits; 0 when it does not.
func (c *Cooldown) Wait(origin string) time.Duration {
	return max(c.until[origin].Sub(c.now()), 0)
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

// Wait is how long until the dialogs unlock; 0 when they are not locked. The lock lifts when fewer than `limit` refusals
// are inside the window, so it is the refusal `limit` places from the newest that must leave it.
func (l *Lockout) Wait() time.Duration {
	recent := l.recent()
	if len(recent) < l.limit {
		return 0
	}
	return max(recent[len(recent)-l.limit].Add(l.window).Sub(l.now()), 0)
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
