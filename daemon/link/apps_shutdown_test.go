package link

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const ignoresTermAgent = `sh -c 'trap "" TERM; echo up; while :; do sleep 1; done'`

// A Detach removes its app from the hub first and then waits for the agents to end. A Shutdown that does not wait for
// it returns while an agent still runs, and the daemon exits with that agent unowned.
func TestShutdownWaitsForADetachThatIsStillEndingItsAgents(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, ignoresTermAgent)
	r.add(s.srv.URL, "tok")
	s.last(1).waitEvents("hello", 1)
	e := r.hub.snapshot()[0]
	h := handler{h: r.hub, e: e}

	opened, err := h.HandleOpen(context.Background(), "s1", "claude")
	if err != nil {
		t.Fatal(err)
	}
	pid := int(opened.Pid)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	waitFor(t, "the agent to be up", func() bool { return e.fleet.Size() == 1 })

	if err := h.HandleStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the detach to have taken the app out of the hub", func() bool { return !r.hub.Has(s.srv.URL) })

	start := time.Now()
	r.hub.Shutdown(5 * time.Second)
	if len(r.emptied()) == 0 {
		t.Errorf("Shutdown returned after %v while a detach was still ending its agents", time.Since(start))
	}
	waitFor(t, "the agent's group to be gone once Shutdown returned", func() bool { return !groupAlive(pid) })
}

// The daemon stops its apps and its pages side by side: the apps must stop taking opens when that begins, not when
// their own turn comes.
func TestAnOpenAfterTheHubDrainedStartsNothingAndLeavesRunningAgentsAlone(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	s.last(1).waitEvents("hello", 1)
	e := r.hub.snapshot()[0]
	h := handler{h: r.hub, e: e}
	if _, err := h.HandleOpen(context.Background(), "s1", "claude"); err != nil {
		t.Fatal(err)
	}

	r.hub.Drain()
	if _, err := h.HandleOpen(context.Background(), "late", "claude"); err == nil || err.Error() != "this daemon is stopping" {
		t.Errorf("an open after Drain: %v", err)
	}
	if err := r.hub.Add(registry.App{Server: "http://127.0.0.1:1", Token: "t"}); err == nil {
		t.Error("an app was added after Drain")
	}
	if e.fleet.Size() != 1 {
		t.Errorf("Drain ended the agent that ran: size %d", e.fleet.Size())
	}
}
