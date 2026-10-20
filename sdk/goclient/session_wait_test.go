package goclient_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goclient"
)

type waited struct {
	code int
	err  error
}

func waitAsync(s *goclient.Session) <-chan waited {
	out := make(chan waited, 1)
	go func() {
		code, err := s.Wait()
		out <- waited{code, err}
	}()
	return out
}

func TestWaitReturnsTheExitCodeOfTheAgent(t *testing.T) {
	d, s, id := openFakeSession(t, nil, "1")
	result := waitAsync(s)
	select {
	case r := <-result:
		t.Fatalf("Wait returned before the agent ended: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	d.say(msg{"t": "frame", "id": id, "kind": "exit", "data": "", "code": 3})
	select {
	case r := <-result:
		if r.code != 3 || r.err != nil {
			t.Fatalf("Wait = %d, %v", r.code, r.err)
		}
	case <-time.After(wait):
		t.Fatal("Wait did not return after the exit frame")
	}
	if code, err := s.Wait(); code != 3 || err != nil { // every call answers the same
		t.Fatalf("second Wait = %d, %v", code, err)
	}
}

func TestWaitIsAnErrorWhenTheSocketEndsBeforeTheAgentExits(t *testing.T) {
	d, s, _ := openFakeSession(t, nil, "1")
	result := waitAsync(s)
	_ = d.conn.Close()
	select {
	case r := <-result:
		if r.code != -1 || !errors.Is(r.err, goclient.ErrClosed) {
			t.Fatalf("Wait = %d, %v", r.code, r.err)
		}
	case <-time.After(wait):
		t.Fatal("Wait did not return when the socket ended")
	}
}

func TestWaitReturnsWhenTheAppClosesTheSession(t *testing.T) {
	_, s, _ := openFakeSession(t, nil, "1")
	result := waitAsync(s)
	_ = s.Close()
	select {
	case r := <-result:
		if r.code != -1 || !errors.Is(r.err, goclient.ErrClosed) {
			t.Fatalf("Wait = %d, %v", r.code, r.err)
		}
	case <-time.After(wait):
		t.Fatal("Wait did not return after Close")
	}
}

func TestWriteLineSendsTheSameAsWrite(t *testing.T) {
	d, s, _ := openFakeSession(t, nil, "1")
	if err := s.WriteLine("one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("two"); err != nil {
		t.Fatal(err)
	}
	d.waitSent("write", "one", "two")
}
