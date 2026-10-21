package ui

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const page = "http://localhost:5173"

// safeBuf is a buffer the logger writes while a test reads it.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testLog is a logger whose output, stdout and stderr together, the test can read.
func testLog() (*proc.Logger, *safeBuf) {
	out := &safeBuf{}
	return proc.NewLogger(out, out), out
}

// minWait is the least waitFor waits: the first run of a fresh script takes seconds when the machine is busy (macOS
// scans it), and a wait ends as soon as its condition holds, so a long one costs a passing test nothing.
const minWait = 20 * time.Second

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(max(timeout, minWait))
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeRegistry saves a registry of the apps' servers and the page origins.
func writeRegistry(t *testing.T, file string, apps, pages []string) {
	t.Helper()
	r := registry.Empty()
	for _, s := range apps {
		r.Apps = append(r.Apps, registry.App{Server: s, Token: "t"})
	}
	for _, p := range pages {
		r.Browsers = append(r.Browsers, registry.BrowserGrant{Origin: p, GrantedAt: registry.GrantStamp(time.Now())})
	}
	if err := registry.Save(file, r); err != nil {
		t.Fatal(err)
	}
}
