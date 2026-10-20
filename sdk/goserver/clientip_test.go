package goserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIPPrefersTheProxyHeaderOnlyWhenItHoldsAnAddress(t *testing.T) {
	tests := []struct {
		name, header, value, remote, want string
	}{
		{"no header configured", "", "203.0.113.9", "10.0.0.1:4000", "10.0.0.1"},
		{"header configured and present", "Fly-Client-IP", "203.0.113.9", "10.0.0.1:4000", "203.0.113.9"},
		{"header is padded", "Fly-Client-IP", " 203.0.113.9 ", "10.0.0.1:4000", "203.0.113.9"},
		{"ipv6 is normalised", "Fly-Client-IP", "2001:DB8:0:0:0:0:0:1", "10.0.0.1:4000", "2001:db8::/64"},
		{"header configured but absent", "Fly-Client-IP", "", "10.0.0.1:4000", "10.0.0.1"},
		{"header is not an address", "Fly-Client-IP", "not-an-ip", "10.0.0.1:4000", "10.0.0.1"},
		{"remote has no port", "", "", "10.0.0.1", "10.0.0.1"},
		{"ipv6 header counts its /64", "Fly-Client-IP", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "10.0.0.1:4000", "2001:db8:1:2::/64"},
		{"ipv6 connection counts its /64", "", "", "[2001:db8:1:2:1::9]:4000", "2001:db8:1:2::/64"},
		{"ipv4-mapped ipv6 is ipv4", "Fly-Client-IP", "::ffff:203.0.113.9", "10.0.0.1:4000", "203.0.113.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/agentd/pair", http.NoBody)
			r.RemoteAddr = tc.remote
			if tc.header != "" && tc.value != "" {
				r.Header.Set(tc.header, tc.value)
			}
			if got := clientIP(r, tc.header); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgedOutClientsLeaveTheFailureMap(t *testing.T) {
	now := time.Now()
	h := New(Options{Now: func() time.Time { return now }})
	for i := range 5 {
		if _, err := h.takeCode("NOPE0-NOPE0", fmt.Sprintf("203.0.113.%d", i)); !errors.Is(err, errBadCode) {
			t.Fatalf("takeCode = %v, want a bad code", err)
		}
	}
	if len(h.failures) != 5 {
		t.Fatalf("failures holds %d clients, want 5", len(h.failures))
	}
	now = now.Add(2 * failureWindow)
	if _, err := h.takeCode("NOPE0-NOPE0", "198.51.100.1"); !errors.Is(err, errBadCode) {
		t.Fatalf("takeCode = %v, want a bad code", err)
	}
	if len(h.failures) != 1 {
		t.Fatalf("failures holds %d clients after the window, want only the new one", len(h.failures))
	}
}
