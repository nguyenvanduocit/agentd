package ui

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// A state shows EVERY saved app with its status, not only the linked ones: an app the daemon dropped (its server sent
// Stop, refused the token, or speaks another protocol) is linked again only at the next start, and until then the
// person has to see it, and see why, to disconnect it.

func TestAppStateOfEachWayAnAppCanBe(t *testing.T) {
	for _, c := range []struct {
		name                         string
		linked, connected            bool
		dropped, detail              string
		wantStatus, wantReasonPrefix string
	}{
		{"connected", true, true, "", "", AppConnected, ""},
		{"linked, not connected yet", true, false, "", "", AppConnecting, ""},
		{"saved, not linked yet, never dropped", false, false, "", "", AppConnecting, ""},
		{"its server sent Stop", false, false, "stopped", "", AppStopped, "The app stopped this connection"},
		{"its server refused the token", false, false, "refused", "", AppRefused, "The app no longer accepts this computer"},
		{"another protocol, with the hub's sentence", false, false, "incompatible", "The app speaks protocol 3 and agentd on this computer speaks 1: one of them needs an update.", AppIncompatible, "The app speaks protocol 3"},
		{"another protocol, no sentence", false, false, "incompatible", "", AppIncompatible, "The app and agentd on this computer"},
		{"an unknown reason is not shown as a drop", false, false, "from-the-future", "", AppConnecting, ""},
	} {
		got := AppStateOf(c.linked, c.connected, c.dropped, c.detail)
		if got.Status != c.wantStatus {
			t.Errorf("%s: status %q, want %q", c.name, got.Status, c.wantStatus)
		}
		if !strings.HasPrefix(got.Reason, c.wantReasonPrefix) || (c.wantReasonPrefix == "" && got.Reason != "") {
			t.Errorf("%s: reason %q, want one starting %q", c.name, got.Reason, c.wantReasonPrefix)
		}
		lower := strings.ToLower(got.Reason)
		for _, jargon := range []string{"daemon", "engine", "origin", "server"} {
			if strings.Contains(lower, jargon) {
				t.Errorf("%s: the reason %q says %q", c.name, got.Reason, jargon)
			}
		}
	}
}

func TestAppRowsListEverySavedAppInTheOrderTheyAreSaved(t *testing.T) {
	status := map[string]AppState{
		"https://a.example": {Status: AppConnected},
		"https://c.example": {Status: AppRefused, Reason: "The app no longer accepts this computer: its access was taken back."},
	}
	rows := AppRows([]string{"https://a.example", "https://b.example", "https://c.example"}, func(s string) AppState {
		if st, ok := status[s]; ok {
			return st
		}
		return AppState{}
	})
	want := []AppRow{
		{Server: "https://a.example", Status: AppConnected},
		{Server: "https://b.example", Status: AppConnecting}, // an empty status is the one of an app not linked yet
		{Server: "https://c.example", Status: AppRefused, Reason: "The app no longer accepts this computer: its access was taken back."},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows %+v, want %+v", rows, want)
	}
	if got := AppRows(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("no saved app: %#v, want an empty list (never null on the wire)", got)
	}
	// With nothing to ask, every saved app is on its way to being linked.
	if got := AppRows([]string{"https://a.example"}, nil); got[0].Status != AppConnecting {
		t.Errorf("without a status function: %+v", got)
	}
}

func TestTheStateCarriesEverySavedAppAndStillTheLinkedOnesAlone(t *testing.T) {
	f := newMenu(t)
	linked, dropped, waiting := "https://a.example", "https://b.example", "https://c.example"
	writeRegistry(t, f.file, []string{linked, dropped, waiting}, nil)
	f.linked[linked] = true
	f.deps.AppStatus = func(server string) AppState {
		switch server {
		case linked:
			return AppState{Status: AppConnected}
		case dropped:
			return AppState{Status: AppRefused, Reason: "The app no longer accepts this computer: its access was taken back."}
		}
		return AppState{Status: AppConnecting}
	}
	f.start(t)
	s := f.link.last(t)
	want := []AppRow{
		{Server: linked, Status: AppConnected},
		{Server: dropped, Status: AppRefused, Reason: "The app no longer accepts this computer: its access was taken back."},
		{Server: waiting, Status: AppConnecting},
	}
	if !reflect.DeepEqual(s.AppList, want) {
		t.Fatalf("appList %+v, want %+v", s.AppList, want)
	}
	// `apps` keeps its meaning, the linked apps, so a tray program of an older build still draws what it drew.
	if !reflect.DeepEqual(s.Apps, []string{linked}) {
		t.Fatalf("apps %v, want only the linked one", s.Apps)
	}
}

func TestWithoutAStatusFunctionTheLinkedAppsAreConnectedAndTheRestAreConnecting(t *testing.T) {
	f := newMenu(t)
	writeRegistry(t, f.file, []string{"https://a.example", "https://b.example"}, nil)
	f.linked["https://a.example"] = true
	f.start(t)
	want := []AppRow{{Server: "https://a.example", Status: AppConnected}, {Server: "https://b.example", Status: AppConnecting}}
	if got := f.link.last(t).AppList; !reflect.DeepEqual(got, want) {
		t.Fatalf("appList %+v, want %+v", got, want)
	}
}

func TestTheStateLineIsAdditive(t *testing.T) {
	// A tray program of an older build reads the fields it knows and ignores appList and notice; a newer one reading a
	// state without them draws from `apps`. So the old fields keep their names, and the new ones are plain additions.
	line := NewState(Facts{Version: "1", Apps: []string{"https://a.example"}, AppList: []AppRow{{Server: "https://a.example", Status: AppConnected}}}).line()
	for _, want := range []string{`"apps":["https://a.example"]`, `"appList":[{"server":"https://a.example","status":"connected"}]`} {
		if !strings.Contains(line, want) {
			t.Errorf("no %s in %s", want, line)
		}
	}
	if strings.Contains(line, `"notice"`) {
		t.Errorf("an empty notice is on the wire: %s", line)
	}
	if empty := NewState(Facts{Version: "1"}).line(); !strings.Contains(empty, `"appList":[]`) {
		t.Errorf("no saved app must be an empty list, not null: %s", empty)
	}
	if with := NewState(Facts{Version: "1", Notice: "Could not disconnect x."}).line(); !strings.Contains(with, `"notice":"Could not disconnect x."`) {
		t.Errorf("the notice is not on the wire: %s", with)
	}
}

// --- the notice: what a choice that failed says, and for how long ---

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestADisconnectThatFailsLeavesANoticeThatLastsAWhileAndASuccessClearsIt(t *testing.T) {
	f := newMenu(t)
	clk := &clock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	f.deps.Now = clk.now // the menu refreshes every 20 ms in this fixture; the notice follows this clock, not the real one
	app := "https://a.example"
	writeRegistry(t, f.file, []string{app}, nil)
	var fail sync.Mutex
	failing := true
	f.deps.Disconnect = func(string) error {
		fail.Lock()
		defer fail.Unlock()
		if failing {
			return errors.New("open /home/u/.config/agentd/daemon.json: permission denied")
		}
		return nil
	}
	f.start(t)
	if n := f.link.last(t).Notice; n != "" {
		t.Fatalf("a notice before anything happened: %q", n)
	}

	f.link.onAction(Action{Kind: ActionDisconnect, Origin: app})
	waitFor(t, "the notice", time.Second, func() bool { return f.link.last(t).Notice != "" })
	want := "Could not disconnect " + app + ": open /home/u/.config/agentd/daemon.json: permission denied."
	if got := f.link.last(t).Notice; got != want {
		t.Fatalf("notice %q, want %q", got, want)
	}

	// It does not vanish with the next refresh: a person needs time to read it.
	clk.add(5 * time.Second)
	time.Sleep(100 * time.Millisecond) // several refreshes later
	if got := f.link.last(t).Notice; got == "" {
		t.Fatal("the notice was gone after five seconds")
	}

	// After its time it is gone by itself.
	clk.add(NoticeFor)
	waitFor(t, "the notice to expire", time.Second, func() bool { return f.link.last(t).Notice == "" })

	// And a success clears it at once: fail again, then succeed.
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: app})
	waitFor(t, "the notice again", time.Second, func() bool { return f.link.last(t).Notice != "" })
	fail.Lock()
	failing = false
	fail.Unlock()
	f.link.onAction(Action{Kind: ActionDisconnect, Origin: app})
	waitFor(t, "a success to clear it", time.Second, func() bool { return f.link.last(t).Notice == "" })
}

func TestAnUpdatesSettingThatCannotBeSavedSaysSo(t *testing.T) {
	f := newMenu(t)
	f.deps.SetUpdates = func(bool) error { return errors.New("settings.json is read-only\nsecond line that must not be shown") }
	f.start(t)
	f.link.onAction(Action{Kind: ActionUpdates, On: false})
	waitFor(t, "the notice", time.Second, func() bool { return f.link.last(t).Notice != "" })
	if got, want := f.link.last(t).Notice, "Could not save the updates setting: settings.json is read-only."; got != want {
		t.Fatalf("notice %q, want %q (one line of the reason)", got, want)
	}
}

func TestACleanedNoticeNeverCarriesAnythingButOneLineOfReason(t *testing.T) {
	long := strings.Repeat("x", 400)
	for in, want := range map[string]string{
		"disk full":         "disk full",
		"first\nsecond":     "first",
		"  padded  ":        "padded",
		"ends with a stop.": "ends with a stop",
		"":                  "no reason was given",
		"\n\n":              "no reason was given",
		long:                strings.Repeat("x", 159) + "…",
	} {
		if got := oneLineOf(errors.New(in)); got != want {
			t.Errorf("oneLineOf(%.20q) = %.30q, want %.30q", in, got, want)
		}
	}
}
