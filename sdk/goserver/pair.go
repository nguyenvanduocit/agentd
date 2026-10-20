package goserver

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// codeTTL is how long a pairing code works.
const codeTTL = 10 * time.Minute

// Wrong codes are counted per client address: after maxFailures within failureWindow that address's pairing attempts
// are refused until the oldest of them ages out. A code has 50 bits, so this makes guessing one hopeless.
const (
	maxFailures   = 10
	failureWindow = time.Minute
)

// codeAlphabet has no 0, 1, I, L, O or U: a code read from a screen is typed without doubt.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

const codeLength = 10

// maxPairBody bounds the body of a pairing request.
const maxPairBody = 4 << 10

var (
	errBadCode     = errors.New("the code is wrong, used or expired")
	errRateLimited = errors.New("too many wrong codes: wait a minute")
)

type pairCode struct {
	text    string // as shown to the user: XXXXX-XXXXX
	userID  string
	expires time.Time
	timer   *time.Timer
}

func newCode() string {
	b := make([]byte, codeLength)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source failing is not recoverable
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b[:5]) + "-" + string(b[5:])
}

// normalizeCode is the form codes are kept and compared in: upper case, without the dash or spaces.
func normalizeCode(s string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
}

// liveCodeLocked is the user's pairing code if it has not expired.
func (h *Server) liveCodeLocked(userID string) *pairCode {
	for _, c := range h.codes {
		if c.userID == userID && h.opts.Now().Before(c.expires) {
			return c
		}
	}
	return nil
}

// dropCodeLocked forgets the user's pairing code, if any.
func (h *Server) dropCodeLocked(userID string) {
	for key, c := range h.codes {
		if c.userID == userID {
			c.timer.Stop()
			delete(h.codes, key)
		}
	}
}

// StartPairing gives the user a new single-use code, replacing the one they had: a user has at most one.
func (h *Server) StartPairing(userID string) (List, error) {
	c := &pairCode{text: newCode(), userID: userID, expires: h.opts.Now().Add(codeTTL)}
	key := normalizeCode(c.text)
	c.timer = time.AfterFunc(codeTTL, func() { h.expire(key, c) })
	h.mu.Lock()
	h.dropCodeLocked(userID) // dropped and replaced in one step: a user never has two codes
	h.codes[key] = c
	h.mu.Unlock()
	h.publish(userID)
	return h.List(userID)
}

// CancelPairing forgets the user's pending code; without one it changes nothing.
func (h *Server) CancelPairing(userID string) (List, error) {
	h.mu.Lock()
	h.dropCodeLocked(userID)
	h.mu.Unlock()
	h.publish(userID)
	return h.List(userID)
}

// expire forgets c once it ran out and tells the user's tabs.
func (h *Server) expire(key string, c *pairCode) {
	h.mu.Lock()
	if h.codes[key] == c {
		delete(h.codes, key)
	}
	h.mu.Unlock()
	h.publish(c.userID)
}

// Machine is what a daemon says about itself when it pairs.
type Machine struct{ Hostname, OS, Arch string }

// Pair exchanges a pairing code for a token. The token is shown once: only its hash is kept. Every pairing adds a
// daemon, named after the machine's hostname; a machine that pairs twice appears twice until the user disconnects
// the first. remoteIP is the requester's IP for per-IP rate limiting.
func (h *Server) Pair(code string, m Machine, remoteIP string) (string, error) {
	userID, err := h.takeCode(normalizeCode(code), remoteIP)
	if err != nil {
		return "", err
	}
	token := "agd_" + newID() + newID()
	_, err = h.opts.Tokens.Add(Record{
		ID: newID(), UserID: userID, TokenHash: hashToken(token), Name: defaultName(m.Hostname), Hostname: m.Hostname, OS: m.OS, Arch: m.Arch,
	})
	if err != nil {
		return "", fmt.Errorf("keep the daemon's token: %w", err)
	}
	h.publish(userID)
	return token, nil
}

// takeCode uses up a code and returns the user it belongs to. A wrong code counts against the
// per-IP failure limit so one attacker cannot deny service to all users.
func (h *Server) takeCode(key, remoteIP string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.opts.Now()
	h.sweepFailures(now)
	recent := trimFailures(h.failures[remoteIP], now)
	if len(recent) >= maxFailures {
		return "", errRateLimited
	}
	c := h.codes[key]
	if c == nil || !now.Before(c.expires) {
		h.failures[remoteIP] = append(recent, now)
		// The address is the limit's key: behind a proxy, this line shows whether the client's header is what counts.
		log.Printf("daemon: pair: wrong pairing code from %s (%d in the last minute)", remoteIP, len(h.failures[remoteIP]))
		return "", errBadCode
	}
	c.timer.Stop()
	delete(h.codes, key)
	return c.userID, nil
}

// sweepFailures drops the clients whose failures have all aged out, at most once per failureWindow, so the map holds
// only the clients that failed within the last two windows however many addresses have come and gone.
func (h *Server) sweepFailures(now time.Time) {
	if now.Sub(h.failuresSwept) < failureWindow {
		return
	}
	h.failuresSwept = now
	for ip, failures := range h.failures {
		if len(trimFailures(failures, now)) == 0 {
			delete(h.failures, ip)
		}
	}
}

func trimFailures(failures []time.Time, now time.Time) []time.Time {
	keep := 0
	for keep < len(failures) && now.Sub(failures[keep]) >= failureWindow {
		keep++
	}
	return failures[keep:]
}

type pairRequest struct {
	Code     string `json:"code"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// maxField bounds the machine fields a daemon may report.
const maxField = 100

func clip(s string) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > maxField {
		return string(r[:maxField])
	}
	return strings.TrimSpace(s)
}

// clientIP is the address the pairing rate limit counts failures against. Behind a proxy, RemoteAddr is the proxy,
// so every client would share one bucket: header names the request header in which the proxy puts the client's
// address (Fly-Client-IP). Without the header, or when it holds no IP address, the connection's own address counts.
// An IPv6 client owns a whole /64, so its bucket is the /64.
func clientIP(r *http.Request, header string) string {
	if header != "" {
		if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get(header))); err == nil {
			return bucket(ip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return bucket(ip)
	}
	return host
}

// bucket is the address as a failure counter keys it: IPv4 as is, IPv6 as its /64 prefix.
func bucket(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

// handlePair is POST /agentd/pair: {code, hostname, os, arch} -> {token}.
func (h *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPairBody)).Decode(&req); err != nil {
		http.Error(w, "the request is not valid JSON", http.StatusBadRequest)
		return
	}
	token, err := h.Pair(req.Code, Machine{Hostname: clip(req.Hostname), OS: clip(req.OS), Arch: clip(req.Arch)}, clientIP(r, h.opts.ClientIPHeader))
	switch {
	case errors.Is(err, errRateLimited):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, errBadCode):
		http.Error(w, err.Error(), http.StatusForbidden)
	case err != nil:
		log.Printf("daemon: pair: %v", err)
		http.Error(w, "pairing failed", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(map[string]string{"token": token}); err != nil {
			log.Printf("daemon: pair: answer: %v", err)
		}
	}
}
