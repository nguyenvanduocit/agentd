// Package goclient is what a Go program uses to find the agentd daemon on the user's computer, ask it to connect, and
// run an agent through it: the Go counterpart of the JavaScript client in sdk/web (@agentd/client), for programs
// that talk to the daemon's local gateway (127.0.0.1, ports 47615 to 47624) as a web page does.
//
// A web page has an origin the browser puts in every request; a Go program has none, so every call takes the app's
// origin explicitly. The gateway accepts an https origin, or an http one on a loopback host, and the daemon connects
// server mode apps at that origin. The protocol and its limits are in daemon/README.md ("Apps, web pages and the
// gateway").
//
// The package depends on the standard library and github.com/gorilla/websocket only.
package goclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	firstPort     = 47615
	portCount     = 10
	detectTimeout = 800 * time.Millisecond
	maxBody       = 1 << 20 // the largest answer read from the daemon
)

// DefaultPorts are the ports a daemon's gateway listens on: the first free one of 47615 to 47624.
func DefaultPorts() []int {
	ports := make([]int, portCount)
	for i := range ports {
		ports[i] = firstPort + i
	}
	return ports
}

// Option changes how a call reaches the daemon. Each call reads the options it uses and ignores the others.
type Option func(*options)

type options struct {
	httpClient *http.Client
	dialer     *websocket.Dialer
	ports      []int
	timeout    time.Duration
}

func newOptions(opts []Option) options {
	o := options{httpClient: http.DefaultClient, dialer: websocket.DefaultDialer, ports: DefaultPorts(), timeout: detectTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithHTTPClient makes the HTTP calls (Detect, ConnectServer, ConnectBrowser, Disconnect) with c.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.httpClient = c } }

// WithDialer opens the session socket (OpenSession) with d.
func WithDialer(d *websocket.Dialer) Option { return func(o *options) { o.dialer = d } }

// WithPorts is where Detect looks (default: DefaultPorts).
func WithPorts(ports ...int) Option { return func(o *options) { o.ports = ports } }

// WithTimeout is how long Detect waits for each port (default 800 ms).
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// AgentInfo is an agent the daemon can start.
type AgentInfo struct {
	Name string
	// Title is what to call the agent on screen ("Claude Code"); its name when the daemon gives none.
	Title  string
	Ready  bool
	Reason string
}

// Granted is what the daemon allowed this origin.
type Granted struct {
	Server  bool `json:"server"`
	Browser bool `json:"browser"`
}

// Detected is the answer of Detect. When Found is false nothing else is set, but Refused: a daemon that answers and
// turns the call down (the origin is wrong, the call came from another user) is there; "no daemon" would send the
// person looking for an install problem that does not exist.
type Detected struct {
	Found   bool
	Port    int
	Version string
	Granted Granted
	// Agents is nil when the daemon sent none (it lists them for an origin it allowed only).
	Agents []AgentInfo
	// Protocol is the protocol the daemon states in its status; 0 when it states none (an older daemon).
	Protocol int
	// Refused is why a daemon on the first port that refused answered no status, when no port answered one: ReasonOrigin,
	// ReasonHost, ReasonPeer ... "" when nothing refused (nothing listens, or another program answers). Since 0.7.0.
	Refused Reason
}

// Daemon is the daemon this answer was found on, as the calls that take one want it (ConnectBrowser, OpenSession).
// Since 0.7.0.
func (d Detected) Daemon() Daemon { return Daemon{Port: d.Port} }

// SupportedProtocols is the protocols of the daemon's gateway that this client speaks.
func SupportedProtocols() []int { return []int{1, 2} }

// Incompatible is a sentence for a person when the daemon states a protocol this client does not speak, and which side
// to update; "" when it states none, or one this client speaks.
func (d Detected) Incompatible() string {
	if !d.Found {
		return ""
	}
	return incompatibility(d.Protocol, SupportedProtocols())
}

func incompatibility(protocol int, supported []int) string {
	if protocol == 0 || slices.Contains(supported, protocol) {
		return ""
	}
	if protocol < slices.Min(supported) {
		return "This agentd is too old for this app. Update agentd."
	}
	return "This agentd is newer than this app understands. The app needs an update."
}

// Daemon is a daemon's gateway on this computer.
type Daemon struct {
	Port int
}

// Reason is why a connection did not happen.
type Reason string

// The words of each reason are in contract/testdata/page-reasons.json, which the daemon, this package and
// @agentd/client are all tested against. A daemon sends a legacy `reason` and, for the cases a newer client can say
// better, a specific `code`; the Reason of a result is the code when this package knows it.
const (
	ReasonDenied    Reason = "denied"   // the user pressed Deny, or closed the dialog
	ReasonTimeout   Reason = "timeout"  // the dialog was not answered in time
	ReasonCooldown  Reason = "cooldown" // the user declined a moment ago; see ConnectResult.RetryAfter
	ReasonBusy      Reason = "busy"     // another request waits for an answer
	ReasonNoDialog  Reason = "no-dialog"
	ReasonPairing   Reason = "pairing" // the app refused the pairing code
	ReasonOrigin    Reason = "origin"  // the daemon does not accept the origin: see ValidateOrigin
	ReasonMalformed Reason = "malformed"
	// ReasonUnreachable is client side: the daemon did not answer.
	ReasonUnreachable Reason = "unreachable"

	// ReasonPeer: the daemon runs as another user than this program. ReasonHost: it did not accept the address used to
	// reach it. ReasonNotFound: it does not know the request. ReasonError: it could not carry the request out. Since
	// 0.7.0 these have words of their own.
	ReasonPeer     Reason = "peer"
	ReasonHost     Reason = "host"
	ReasonNotFound Reason = "not-found"
	ReasonError    Reason = "error"

	// ReasonLocked: several requests were declined in a row and the daemon does not ask for a while (legacy reason
	// cooldown). ReasonNoFolder: Allow was pressed with no folder chosen (legacy no-dialog). ReasonNotSaved: the daemon
	// could not save the connection (legacy pairing). Since 0.7.0.
	ReasonLocked   Reason = "locked"
	ReasonNoFolder Reason = "no-folder"
	ReasonNotSaved Reason = "not-saved"

	// ReasonCancelled is client side: the context ended while the user's dialog was open. The dialog may still be
	// open, and answering Allow there still connects: a later Detect finds the grant. Since 0.7.0.
	ReasonCancelled Reason = "cancelled"
)

// ConnectResult is the daemon's answer to a connection request. Reason and Message are set when Granted is false.
type ConnectResult struct {
	Granted bool
	Reason  Reason
	// Message is the daemon's own words about a refusal, when it gave any.
	Message string
	// RetryAfter is how long the daemon asks to wait before the request is made again (a cooldown, a lock); 0 when it
	// says none. Since 0.7.0.
	RetryAfter time.Duration
}

func base(d Daemon) string { return "http://127.0.0.1:" + strconv.Itoa(d.Port) }

// wireAgent is an agent as the daemon sends it: a 0.2 daemon sends no title.
type wireAgent struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

func agentInfo(a wireAgent) AgentInfo {
	title := a.Title
	if title == "" {
		title = a.Name
	}
	return AgentInfo{Name: a.Name, Title: title, Ready: a.Ready, Reason: a.Reason}
}

type wireStatus struct {
	Name     string       `json:"name"`
	Version  *string      `json:"version"`
	Granted  *Granted     `json:"granted"`
	Agents   *[]wireAgent `json:"agents"`
	Protocol *float64     `json:"protocol"`
}

func get(ctx context.Context, c *http.Client, url, origin string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", origin)
	return c.Do(req)
}

// probe asks one port for its status; anything but an agentd's answer is not found: nothing listens there, the
// connection was refused, or another program answers.
func probe(ctx context.Context, port int, origin string, o options) Detected {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	res, err := get(ctx, o.httpClient, base(Daemon{port})+"/v1/status", origin)
	if err != nil {
		return Detected{}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Detected{Refused: gatewayRefusal(res.Body)}
	}
	var body wireStatus
	if json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&body) != nil || body.Name != "agentd" || body.Version == nil {
		return Detected{}
	}
	found := Detected{Found: true, Port: port, Version: *body.Version}
	if body.Protocol != nil {
		found.Protocol = int(*body.Protocol)
	}
	if body.Granted != nil {
		found.Granted = *body.Granted
	}
	if body.Agents != nil {
		found.Agents = make([]AgentInfo, len(*body.Agents))
		for i, a := range *body.Agents {
			found.Agents[i] = agentInfo(a)
		}
	}
	return found
}

// gatewayRefusal is the reason in the body of a gateway's refusal ({"granted":false,"reason":"origin"}); "" when the
// body is anything else, so another program's error page is not taken for a daemon's.
func gatewayRefusal(body io.Reader) Reason {
	var refusal struct {
		Granted *bool   `json:"granted"`
		Reason  *string `json:"reason"`
	}
	if json.NewDecoder(io.LimitReader(body, maxBody)).Decode(&refusal) != nil || refusal.Granted == nil || *refusal.Granted || refusal.Reason == nil {
		return ""
	}
	return Reason(*refusal.Reason)
}

// Detect looks for a daemon on this computer, on every port at once, and answers the first port (in the order given)
// that answers like an agentd. Nothing listening, a bad answer and a port that does not answer in time all mean not
// found; a port that answers with a refusal is not found too, with the reason in Detected.Refused (when no other port
// has a daemon). ctx ends the wait. An origin the gateway would refuse is Refused with ReasonOrigin, without a call
// (see ValidateOrigin).
func Detect(ctx context.Context, origin string, opts ...Option) Detected {
	if ValidateOrigin(origin) != nil {
		return Detected{Refused: ReasonOrigin}
	}
	o := newOptions(opts)
	results := make([]Detected, len(o.ports))
	var wg sync.WaitGroup
	for i, port := range o.ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = probe(ctx, port, origin, o)
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.Found {
			return r
		}
	}
	for _, r := range results { // no daemon answered: the first that turned the call down says why
		if r.Refused != "" {
			return r
		}
	}
	return Detected{}
}

// wireResult is the body of /v1/connect, and the wait the answer asks for.
type wireResult struct {
	Granted bool `json:"granted"`
	// Reason is the legacy reason every client knows; Code, when there is one, is the specific one.
	Reason     Reason   `json:"reason"`
	Code       Reason   `json:"code"`
	Message    string   `json:"message"`
	RetryAfter *float64 `json:"retryAfter"`

	retryAfter time.Duration // from the body, else from the Retry-After header; 0 when neither says
}

// reason is what the result is told as: the specific code when this package has words for it, else the legacy reason,
// else malformed.
func (w wireResult) reason() Reason {
	if _, known := reasonTexts[w.Code]; known && w.Code != "" {
		return w.Code
	}
	if w.Reason != "" {
		return w.Reason
	}
	return ReasonMalformed
}

// reasonOrEmpty is reason, but "" for an answer that names none: a refusal of Disconnect without a reason says so.
func (w wireResult) reasonOrEmpty() Reason {
	if w.Code == "" && w.Reason == "" {
		return ""
	}
	return w.reason()
}

// seconds is a wait of whole seconds as a Duration; 0 for a value that is no usable wait.
func seconds(v float64) time.Duration {
	if math.IsNaN(v) || v <= 0 || v > 24*60*60 {
		return 0
	}
	return time.Duration(math.Ceil(v)) * time.Second
}

// connectCall is one request to /v1/connect and what came back: the status and the body (empty when it was no JSON).
func connectCall(ctx context.Context, d Daemon, origin, method, body string, o options) (int, wireResult, error) {
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base(d)+"/v1/connect", payload)
	if err != nil {
		return 0, wireResult{}, err
	}
	req.Header.Set("Origin", origin)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := o.httpClient.Do(req)
	if err != nil {
		return 0, wireResult{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var answer wireResult
	if json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&answer) != nil {
		answer = wireResult{}
	}
	if answer.RetryAfter != nil {
		answer.retryAfter = seconds(*answer.RetryAfter)
	}
	if header, err := strconv.ParseFloat(strings.TrimSpace(res.Header.Get("Retry-After")), 64); answer.retryAfter == 0 && err == nil {
		answer.retryAfter = seconds(header)
	}
	return res.StatusCode, answer, nil
}

func ok(status int) bool { return status >= 200 && status <= 299 }

func connectRequest(ctx context.Context, d Daemon, origin, body string, o options) ConnectResult {
	if err := ValidateOrigin(origin); err != nil {
		return ConnectResult{Reason: ReasonOrigin, Message: err.Error()}
	}
	status, answer, err := connectCall(ctx, d, origin, http.MethodPost, body, o)
	if err != nil {
		if ctx.Err() != nil { // the caller stopped waiting; the daemon's dialog may still be open
			return ConnectResult{Reason: ReasonCancelled, Message: err.Error()}
		}
		return ConnectResult{Reason: ReasonUnreachable, Message: err.Error()}
	}
	if ok(status) && answer.Granted {
		return ConnectResult{Granted: true}
	}
	return ConnectResult{Reason: answer.reason(), Message: answer.Message, RetryAfter: answer.retryAfter}
}

// stringify is the JSON text of v as JSON.stringify writes it: no HTML escaping of <, > and &.
func stringify(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("goclient: encoding a message: " + err.Error()) // v is made of strings: it always encodes
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// ConnectServer asks the user (in a dialog on their machine) to let the daemon connect to the app's own server with
// this pairing code. The server is at origin: the daemon refuses to connect to any other. name is what the app calls
// itself: the dialog and the daemon's menu show it beside origin, and the daemon refuses a request without one. It
// waits for the user's answer; ctx ends the wait.
func ConnectServer(ctx context.Context, d Daemon, origin, name, code string, opts ...Option) ConnectResult {
	body := stringify(struct {
		Mode string `json:"mode"`
		Name string `json:"name"`
		Code string `json:"code"`
	}{"server", name, code})
	return connectRequest(ctx, d, origin, body, newOptions(opts))
}

// ConnectBrowser asks the user to let this origin run agents on their machine directly (OpenSession). name is what the
// app calls itself, as for ConnectServer.
func ConnectBrowser(ctx context.Context, d Daemon, origin, name string, opts ...Option) ConnectResult {
	body := stringify(struct {
		Mode string `json:"mode"`
		Name string `json:"name"`
	}{"browser", name})
	return connectRequest(ctx, d, origin, body, newOptions(opts))
}

// DisconnectError is the daemon's refusal of a Disconnect: it answered, and did not confirm.
type DisconnectError struct {
	Status  int
	Reason  Reason
	Message string
}

func (e *DisconnectError) Error() string {
	return fmt.Sprintf("the daemon refused (HTTP %d, %s): %s", e.Status, e.Reason, e.Message)
}

// Disconnect takes back what this origin was allowed: its browser grant and the app at the origin. Open sessions end.
// The error says why the daemon did not confirm: it did not answer, or it answered with a refusal (a *DisconnectError,
// which errors.As finds); nil when it confirmed.
func Disconnect(ctx context.Context, d Daemon, origin string, opts ...Option) error {
	if err := ValidateOrigin(origin); err != nil {
		return err
	}
	status, answer, err := connectCall(ctx, d, origin, http.MethodDelete, "", newOptions(opts))
	if err != nil {
		return fmt.Errorf("the daemon did not answer: %w", err)
	}
	if !ok(status) {
		return &DisconnectError{Status: status, Reason: answer.reasonOrEmpty(), Message: answer.Message}
	}
	return nil
}

// reasonTexts are the words of each reason: the texts of contract/testdata/page-reasons.json, which a test compares.
var reasonTexts = map[Reason]string{
	ReasonDenied:      "You declined. The app stays disconnected from this computer.",
	ReasonTimeout:     "The dialog on this computer was not answered in time. Try again and answer it.",
	ReasonCooldown:    "You declined a moment ago. Wait half a minute before asking again.",
	ReasonLocked:      "Several connection requests were declined in a row, so agentd is not asking for a while. Try again in a few minutes.",
	ReasonBusy:        "Another connection request is waiting for an answer on this computer. Answer it first.",
	ReasonNoDialog:    "This computer cannot show the confirmation dialog (no graphical session), so nothing was connected.",
	ReasonNoFolder:    "No folder was chosen, so nothing was connected. Choose at least one folder to allow.",
	ReasonPairing:     "The app refused the pairing code. Start the connection again to get a fresh one.",
	ReasonNotSaved:    "agentd could not save the connection on this computer. Look at its log, then try again.",
	ReasonOrigin:      "agentd does not accept requests from this address. Use the address the app is served from, without a path.",
	ReasonHost:        "agentd did not accept the address used to reach it. Open the page from its usual address.",
	ReasonMalformed:   "agentd did not understand the request. Update agentd and try again.",
	ReasonNotFound:    "This agentd does not know the request. Update agentd and try again.",
	ReasonError:       "agentd could not carry out the request. Look at its log, then try again.",
	ReasonPeer:        "agentd runs as another user than this program, so it refuses the request.",
	ReasonUnreachable: "agentd on this computer did not answer. Check that it is running.",
	ReasonCancelled:   "You stopped waiting. The dialog may still be open: answering Allow there still connects.",
}

// ReasonText is a sentence for a person about why a connection did not happen. A reason this package does not know (a
// newer daemon's) gets a sentence that names it.
func ReasonText(r Reason) string {
	if text, ok := reasonTexts[r]; ok {
		return text
	}
	return fmt.Sprintf("agentd on this computer refused the request (%s).", r)
}
