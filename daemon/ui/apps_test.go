package ui

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
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
	rows := AppRows(appsAt("https://a.example", "https://b.example", "https://c.example"), func(s string) AppState {
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
	if got := AppRows(appsAt("https://a.example"), nil); got[0].Status != AppConnecting {
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
	// `apps` stays the linked apps only.
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

func TestTheStateNeverHoldsANilList(t *testing.T) {
	empty := NewState(Facts{Version: "1"})
	if empty.Apps == nil || empty.AppList == nil || empty.Pages == nil || empty.Agents == nil {
		t.Errorf("a list is nil: %+v", empty)
	}
	with := NewState(Facts{Version: "1", Notice: "Could not disconnect x."})
	if with.Notice != "Could not disconnect x." {
		t.Errorf("notice %q", with.Notice)
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

// appsAt are saved apps, one for each server.
func appsAt(servers ...string) []registry.App {
	apps := make([]registry.App, len(servers))
	for i, s := range servers {
		apps[i] = registry.App{Server: s, Token: "t"}
	}
	return apps
}

func TestTheStateCarriesTheNameAndEndOfEachAppAndPage(t *testing.T) {
	f := newMenu(t)
	end := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	r := registry.Empty().
		AddApp(registry.App{Server: "https://a.example", Token: "t", Name: "AIOcean", ExpiresAt: registry.Expiry(time.Now(), 48*time.Hour)}).
		AddApp(registry.App{Server: "https://b.example", Token: "t", Name: "Notes", ExpiresAt: registry.Never}).
		AddApp(registry.App{Server: "https://c.example", Token: "t"}).
		AddBrowser(page, time.Now(), registry.Access{Name: "Docs", ExpiresAt: registry.Expiry(time.Now(), 48*time.Hour)})
	if err := registry.Save(f.file, r); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	s := f.link.last(t)
	names := []string{s.AppList[0].Name, s.AppList[1].Name, s.AppList[2].Name}
	if !reflect.DeepEqual(names, []string{"AIOcean", "Notes", ""}) {
		t.Errorf("names %q", names)
	}
	if got := s.AppList[0].Ends; got.Sub(end).Abs() > 2*time.Second {
		t.Errorf("the first app ends %v, want about %v", got, end)
	}
	if !s.AppList[1].Ends.IsZero() || !s.AppList[2].Ends.IsZero() {
		t.Errorf("an app with no end carries one: %v, %v", s.AppList[1].Ends, s.AppList[2].Ends)
	}
	if len(s.PageList) != 1 || s.PageList[0].Origin != page || s.PageList[0].Name != "Docs" || s.PageList[0].Ends.Sub(end).Abs() > 2*time.Second {
		t.Errorf("pages %+v", s.PageList)
	}
}

// An app whose access ended is not connected any more: it must not be listed with a Disconnect it does not need.
func TestTheStateLeavesOutAnAppWhoseAccessEnded(t *testing.T) {
	f := newMenu(t)
	r := registry.Empty().
		AddApp(registry.App{Server: "https://old.example", Token: "t", ExpiresAt: registry.Expiry(time.Now().Add(-2*time.Hour), time.Hour)}).
		AddApp(registry.App{Server: "https://a.example", Token: "t"})
	if err := registry.Save(f.file, r); err != nil {
		t.Fatal(err)
	}
	f.linked["https://old.example"] = true
	f.start(t)
	s := f.link.last(t)
	if len(s.AppList) != 1 || s.AppList[0].Server != "https://a.example" || len(s.Apps) != 0 {
		t.Fatalf("appList %+v, apps %v: only the app whose access runs is listed", s.AppList, s.Apps)
	}
}
