package link

import (
	"strings"
	"testing"
)

// The hub remembers, for the rest of the run, why it let an app go. The menu shows every saved app, and a saved app
// that the hub dropped (the server sent Stop, refused the token, or speaks another protocol) is linked again only at
// the next start: until then the person has to see it, and see why, to disconnect it.

func TestStatusOfAnAppThatIsConnectedAndOneThatCannotBeReached(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	s.last(1).waitEvents("hello", 1)
	waitFor(t, "connected", func() bool { return r.hub.Status(s.srv.URL).Connected })
	if got := r.hub.Status(s.srv.URL); !got.Linked || got.Dropped != "" {
		t.Fatalf("connected app: %+v", got)
	}

	const nowhere = "http://127.0.0.1:1" // nothing listens: the link keeps retrying
	r.add(nowhere, "tok")
	if got := r.hub.Status(nowhere); !got.Linked || got.Connected || got.Dropped != "" {
		t.Fatalf("an app that cannot be reached: %+v, want linked, not connected, not dropped", got)
	}
	if got := r.hub.Status("http://nobody.example"); got.Linked || got.Connected || got.Dropped != "" {
		t.Fatalf("an app the hub never had: %+v", got)
	}
}

func TestStatusRemembersWhyAnAppWasDropped(t *testing.T) {
	stopped, refused, odd, kept := newFakeServer(t), newFakeServer(t), newFakeServer(t), newFakeServer(t)
	refused.set(func() { refused.refuse = Refused })
	odd.set(func() { odd.protocol = 2 })
	r := newRig(t, echoAgent)
	r.add(kept.srv.URL, "tk") // one app stays, so the hub is never empty
	r.add(stopped.srv.URL, "ts")
	r.add(refused.srv.URL, "wrong")
	r.add(odd.srv.URL, "to")

	stopped.last(1).waitEvents("hello", 1)
	stopped.last(1).emit("stop")

	waitFor(t, "the three drops", func() bool {
		return r.hub.Status(stopped.srv.URL).Dropped != "" && r.hub.Status(refused.srv.URL).Dropped != "" && r.hub.Status(odd.srv.URL).Dropped != ""
	})
	if got := r.hub.Status(stopped.srv.URL); got.Dropped != DropStopped || got.Linked || got.Connected {
		t.Errorf("an app whose server sent Stop: %+v", got)
	}
	if got := r.hub.Status(refused.srv.URL); got.Dropped != DropRefused || got.Linked {
		t.Errorf("an app whose server refused the token: %+v", got)
	}
	got := r.hub.Status(odd.srv.URL)
	if got.Dropped != DropIncompatible || got.Linked {
		t.Errorf("an app of another protocol: %+v", got)
	}
	// The sentence names both sides' protocols, in words a person can pass on to the app's maker.
	if !strings.Contains(got.Detail, "protocol 2") || !strings.Contains(got.Detail, "1") || strings.Contains(strings.ToLower(got.Detail), "daemon") {
		t.Errorf("the incompatible sentence is %q", got.Detail)
	}
	if got := r.hub.Status(kept.srv.URL); got.Dropped != "" || !got.Linked {
		t.Errorf("the app that stayed: %+v", got)
	}
}

func TestAnAppAddedAgainIsNoLongerDropped(t *testing.T) {
	s, kept := newFakeServer(t), newFakeServer(t)
	s.set(func() { s.refuse = Refused })
	r := newRig(t, echoAgent)
	r.add(kept.srv.URL, "tk")
	r.add(s.srv.URL, "revoked")
	waitFor(t, "refused", func() bool { return r.hub.Status(s.srv.URL).Dropped == DropRefused })

	s.set(func() { s.refuse = "" }) // paired again: the server accepts
	r.add(s.srv.URL, "fresh")
	if got := r.hub.Status(s.srv.URL); got.Dropped != "" || !got.Linked {
		t.Fatalf("after adding it again: %+v, want linked and not dropped", got)
	}
}

func TestDisconnectingADroppedAppForgetsWhyItWasDropped(t *testing.T) {
	s, kept := newFakeServer(t), newFakeServer(t)
	s.set(func() { s.refuse = Refused })
	r := newRig(t, echoAgent)
	r.add(kept.srv.URL, "tk")
	r.add(s.srv.URL, "revoked")
	waitFor(t, "refused", func() bool { return r.hub.Status(s.srv.URL).Dropped == DropRefused })

	r.hub.Detach(s.srv.URL, true) // what the user's Disconnect does: the hub no longer has it, the record goes too
	if got := r.hub.Status(s.srv.URL); got.Dropped != "" || got.Linked {
		t.Fatalf("after the user disconnected it: %+v", got)
	}
}
