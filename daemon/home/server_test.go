package home

import (
	"strings"
	"testing"
)

func TestServerOrigin(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://mc.example.com", "https://mc.example.com"},
		{"https://mc.example.com/", "https://mc.example.com"},
		{"http://127.0.0.1:8080/app?x=1#y", "http://127.0.0.1:8080"},
		{"wss://mc.example.com/socket.io", "https://mc.example.com"},
		{"ws://localhost:3000", "http://localhost:3000"},
		{"http://[::1]:9000/x", "http://[::1]:9000"},
		{"http://127.1.2.3:80", "http://127.1.2.3"},
		{"https://MC.Example.com:443/x", "https://mc.example.com"},
		{"https://mc.example.com:8443", "https://mc.example.com:8443"},
		{"wss://mc.example.com:443", "https://mc.example.com"},
		{"https://user:pw@mc.example.com/p", "https://mc.example.com"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ServerOrigin(tc.raw)
			if err != nil || got != tc.want {
				t.Errorf("ServerOrigin = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestServerOriginRejectsWhatIsNotAServerAddress(t *testing.T) {
	for _, tc := range []struct{ raw, msg string }{
		{"mc.example.com", "is not a URL"},
		{"https://", "is not a URL"},
		{"ftp://x", "must start with https://"},
	} {
		if _, err := ServerOrigin(tc.raw); err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("ServerOrigin(%q) error = %v, want one that says %q", tc.raw, err, tc.msg)
		}
	}
}

func TestServerOriginPlaintextIsForLoopbackOnly(t *testing.T) {
	for _, raw := range []string{"http://mc.example.com", "ws://10.0.0.5:8080", "http://127.0.0.1.evil.com", "http://localhost.evil.com"} {
		if _, err := ServerOrigin(raw); err == nil || !strings.Contains(err.Error(), "must use https://") {
			t.Errorf("ServerOrigin(%q) error = %v, want a refusal of plaintext", raw, err)
		}
	}
}

func TestFixedPathsOnTheOrigin(t *testing.T) {
	if got := NamespaceURL("https://s"); got != "https://s/agentd" {
		t.Errorf("NamespaceURL = %q", got)
	}
	if got := PairURL("https://s"); got != "https://s/agentd/pair" {
		t.Errorf("PairURL = %q", got)
	}
}
