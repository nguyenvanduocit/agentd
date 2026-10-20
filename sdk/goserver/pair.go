package goserver

import (
	"context"
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

// codeDraws bounds how many codes StartPairing draws when the store says one is already taken.
const codeDraws = 8

// displayCode is the canonical code as the user reads it, XXXXX-XXXXX.
func displayCode(canonical string) string {
	if len(canonical) == codeLength {
		return canonical[:5] + "-" + canonical[5:]
	}
	return canonical
}

// armCodeTimer tells this instance's tabs when a code it issued runs out. The store is the record of the code; the
// timer only publishes. A code redeemed on another instance stays on these tabs until this fires or Refresh is called.
func (h *Server) armCodeTimer(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.codeTimers[userID]; t != nil {
		t.Stop()
	}
	h.codeTimers[userID] = time.AfterFunc(codeTTL, func() { h.publish(userID) })
}

func (h *Server) disarmCodeTimer(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.codeTimers[userID]; t != nil {
		t.Stop()
		delete(h.codeTimers, userID)
	}
}

func pairingDown(err error) error {
	return fmt.Errorf("%w: %w", ErrPairingUnavailable, err)
}

// StartPairing gives the user a new single-use code, replacing the one they had: a user has at most one.
func (h *Server) StartPairing(userID string) (List, error) {
	now := h.opts.Now()
	expires := now.Add(codeTTL)
	var issued bool
	for range codeDraws {
		text := newCode()
		err := h.opts.Pairing.Issue(context.Background(), userID, normalizeCode(text), now, expires)
		if errors.Is(err, ErrCodeTaken) {
			continue
		}
		if err != nil {
			return List{}, pairingDown(err)
		}
		issued = true
		break
	}
	if !issued {
		return List{}, ErrPairingUnavailable
	}
	h.armCodeTimer(userID)
	h.publish(userID)
	return h.List(userID)
}

// CancelPairing forgets the user's pending code; without one it changes nothing.
func (h *Server) CancelPairing(userID string) (List, error) {
	if err := h.opts.Pairing.Cancel(context.Background(), userID); err != nil {
		return List{}, pairingDown(err)
	}
	h.disarmCodeTimer(userID)
	h.publish(userID)
	return h.List(userID)
}

// Machine is what a daemon says about itself when it pairs.
type Machine struct {
	// Hostname is the machine's name; it becomes the daemon's name until the user renames it.
	Hostname string
	// OS is the operating system ("darwin", "linux").
	OS string
	// Arch is the processor architecture ("arm64", "amd64").
	Arch string
}

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
// per-IP failure limit so one attacker cannot deny service to all users. The count is read before the code is used,
// so a store that cannot be asked pairs nobody.
func (h *Server) takeCode(key, remoteIP string) (string, error) {
	now := h.opts.Now()
	ctx := context.Background()
	n, err := h.opts.Pairing.Failures(ctx, remoteIP, now, failureWindow)
	if err != nil {
		return "", pairingDown(err)
	}
	if n >= maxFailures {
		return "", errRateLimited
	}
	userID, err := h.opts.Pairing.Redeem(ctx, key, now)
	if errors.Is(err, ErrCodeNotFound) {
		count, ferr := h.opts.Pairing.Fail(ctx, remoteIP, now, failureWindow)
		if ferr != nil {
			log.Printf("daemon: pair: wrong pairing code from %s (the failure was not recorded: %v)", remoteIP, ferr)
			return "", errBadCode
		}
		// The address is the limit's key: behind a proxy, this line shows whether the client's header is what counts.
		log.Printf("daemon: pair: wrong pairing code from %s (%d in the last minute)", remoteIP, count)
		return "", errBadCode
	}
	if err != nil {
		return "", pairingDown(err)
	}
	h.disarmCodeTimer(userID)
	return userID, nil
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
	case errors.Is(err, ErrPairingUnavailable):
		w.Header().Set("Retry-After", "10")
		http.Error(w, "pairing is unavailable right now: try again", http.StatusServiceUnavailable)
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
