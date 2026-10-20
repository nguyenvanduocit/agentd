package ui

import (
	"bufio"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeApp is the app's end of the channel: it reads the lines the daemon sends and writes choices.
type fakeApp struct {
	conn net.Conn
	got  *lineLog
}

func newPair(t *testing.T) (daemonEnd net.Conn, app *fakeApp) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	app = &fakeApp{conn: b, got: &lineLog{}}
	go func() {
		in := bufio.NewScanner(b)
		for in.Scan() {
			app.got.add(in.Text())
		}
	}()
	return a, app
}

func (a *fakeApp) choose(t *testing.T, line string) {
	t.Helper()
	if _, err := a.conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

func TestAppLinkSendsItsStateToTheApp(t *testing.T) {
	conn, app := newPair(t)
	log, _ := testLog()
	l := NewAppLink(conn, func(Action) {}, func() {}, log)
	l.Update(NewState(baseFacts)) // before Start: held, not sent
	l.Start()
	waitFor(t, "the state sent at Start", 2*time.Second, func() bool { return app.got.hasState(func(s State) bool { return s.Version == "1" }) })
	l.Update(NewState(baseFacts)) // unchanged: nothing
	f := baseFacts
	f.Sessions = 3
	l.Update(NewState(f))
	waitFor(t, "the changed state", 2*time.Second, func() bool { return app.got.hasState(func(s State) bool { return s.Sessions == 3 }) })
	if n := len(app.got.all()); n != 2 {
		t.Fatalf("the app got %d lines %v, want 2: only a change is sent", n, app.got.all())
	}
}

func TestAppLinkDoesWhatTheAppChooses(t *testing.T) {
	conn, app := newPair(t)
	log, _ := testLog()
	actions := make(chan Action, 4)
	l := NewAppLink(conn, func(a Action) { actions <- a }, func() {}, log)
	l.Start()
	app.choose(t, `{"t":"action","id":"disconnect:`+page+`"}`)
	app.choose(t, `not json`)
	app.choose(t, `{"t":"action","id":"format-disk"}`)
	app.choose(t, `{"t":"action","id":"stop"}`)
	for _, want := range []Action{{Kind: ActionDisconnect, Origin: page}, {Kind: ActionStop}} {
		select {
		case got := <-actions:
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %+v", want)
		}
	}
	select {
	case got := <-actions:
		t.Fatalf("an unknown line became %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAppLinkEndsTheDaemonWhenTheAppGoes(t *testing.T) {
	conn, app := newPair(t)
	log, _ := testLog()
	gone := make(chan struct{}, 2)
	l := NewAppLink(conn, func(Action) {}, func() { gone <- struct{}{} }, log)
	l.Start()
	_ = app.conn.Close()
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("onGone was not called when the app closed its end")
	}
	l.Update(NewState(baseFacts)) // a state for an app that is gone is dropped, quietly
	select {
	case <-gone:
		t.Fatal("onGone was called twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAppLinkStopIsNotTheAppGoing(t *testing.T) {
	conn, app := newPair(t)
	log, _ := testLog()
	var gone atomic.Int32
	l := NewAppLink(conn, func(Action) {}, func() { gone.Add(1) }, log)
	l.Start()
	l.Stop()
	time.Sleep(50 * time.Millisecond)
	if gone.Load() != 0 {
		t.Fatal("onGone was called for a Stop")
	}
	l.Update(NewState(baseFacts))
	if n := len(app.got.all()); n != 0 {
		t.Fatalf("a stopped link sent %d lines", n)
	}
}

func TestAppLinkTakesTheChannelFromAnInheritedDescriptor(t *testing.T) {
	testFDConn(t)
}
