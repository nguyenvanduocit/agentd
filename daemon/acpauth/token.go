// Package acpauth is the secret a program on this computer presents to use agentd's standard ACP endpoint (/acp), the
// files that keep it and the address of the endpoint, and `agentd acp`, which prints the ready-made address.
//
// A request to /acp with an Origin header is a web page and needs a grant. One without is not a browser, but it is not
// necessarily a program that could start an agent itself: a dev container, a sandboxed app or an `ssh -R` tunnel reach
// 127.0.0.1 through a forwarder that runs as the user. The token tells the user's own programs from those: it lives in a
// file that only the user's processes on the host can read.
//
// token.go is the pure part (what a token is, how it is compared); files.go and command.go touch the disk.
package acpauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
)

const (
	// TokenBytes is how much randomness a token holds: 256 bits.
	TokenBytes = 32
	// TokenLen is the length of a token as text: TokenBytes in base64url, without padding.
	TokenLen = 43
)

// New is a new token: TokenBytes from the system's random source, as URL-safe text, which needs no escaping in the
// query of an address.
func New() string {
	var b [TokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("acpauth: no randomness: " + err.Error()) // the system's source failing is not something to continue from
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Valid is whether s has the shape of a token: TokenLen characters of the URL-safe alphabet.
func Valid(s string) bool {
	if len(s) != TokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Equal is whether presented is the token want. Nothing is equal to a want that is not a token (an unreadable file
// must not let the empty string in), a length that differs is refused before the bytes are looked at, and the bytes
// are compared in constant time.
func Equal(want, presented string) bool {
	if !Valid(want) || len(presented) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(presented)) == 1
}
