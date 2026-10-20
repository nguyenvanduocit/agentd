package link

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// A dropped socket ends every agent of its app. An `open` the server sent while the socket was up can still be on
// its way to the fleet when that happens: it started its agent after the agents were ended, for a connection that was
// gone, and nobody ever answered or closed it.
func TestAnOpenThatReachesTheFleetAfterTheSocketDroppedStartsNothing(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	s.last(1).waitEvents("hello", 1)
	e := r.hub.snapshot()[0]

	connection, drop := context.WithCancel(context.Background())
	drop()           // the connection ended: its context is done ...
	e.fleet.EndAll() // ... and OnDisconnect ended the agents, of which this open was not one yet

	if _, err := (handler{h: r.hub, e: e}).HandleOpen(connection, "late", "claude"); err == nil {
		t.Error("an open of a connection that had ended started an agent")
	}
	waitFor(t, "no agent to be left for the dead connection", func() bool { return e.fleet.Size() == 0 })
}

// The same, with the drop and the opens running at once, in the order the link takes: the connection's context ends,
// then the agents are ended.
func TestDroppingTheSocketWhileOpensAreInFlightLeavesNoAgent(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	s.last(1).waitEvents("hello", 1)
	e := r.hub.snapshot()[0]
	h := handler{h: r.hub, e: e}

	for round := range 12 {
		connection, drop := context.WithCancel(context.Background())
		var opens sync.WaitGroup
		for n := range 3 {
			opens.Add(1)
			go func() {
				defer opens.Done()
				_, _ = h.HandleOpen(connection, fmt.Sprintf("r%d-%d", round, n), "claude")
			}()
		}
		drop()
		e.fleet.EndAll()
		opens.Wait()
		waitFor(t, fmt.Sprintf("round %d to leave no agent", round), func() bool { return e.fleet.Size() == 0 })
	}
}
