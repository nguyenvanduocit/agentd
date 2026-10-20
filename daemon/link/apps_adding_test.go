package link

import (
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// The hub is not empty while an app is being added: when the daemon starts with two saved apps and the first one's
// token is refused, that app is detached while the second is still having its wrapper written. Counting only the
// entries made the hub look empty, OnEmpty ended the daemon with the exit code that means "stay stopped", and the
// second app, whose token is good, was never served.
func TestAnAppThatIsBeingAddedKeepsTheHubFromBeingEmpty(t *testing.T) {
	const first, second = "http://127.0.0.1:1", "http://127.0.0.1:2" // nothing listens: the links only keep retrying
	r := newRig(t, "cat")
	r.add(first, "token-a")

	inWindow, release := make(chan struct{}), make(chan struct{})
	r.hub.afterWrapper = func() { close(inWindow); <-release }
	added := make(chan error, 1)
	go func() { added <- r.hub.Add(registry.App{Server: second, Token: "token-b"}) }()
	<-inWindow // the second app is half added: its wrapper exists, it is not in the hub

	r.hub.Detach(first, false) // what a refused token does to the first app
	if got := r.emptied(); len(got) != 0 {
		t.Fatalf("OnEmpty ran %v while an app was being added", got)
	}

	close(release)
	if err := <-added; err != nil {
		t.Fatal(err)
	}
	if !r.hub.Has(second) {
		t.Fatal("the app that was being added is not in the hub")
	}

	r.hub.Detach(second, false) // now the last app really leaves
	if got := r.emptied(); len(got) != 1 {
		t.Fatalf("OnEmpty ran %v when the last app left, want once", got)
	}
}
