package ui

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nguyenvanduocit/agentd/daemon/pagetools"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const refreshEvery = 2 * time.Second

// NoticeFor is how long a sentence about a choice that failed stays on the state, long enough to read.
const NoticeFor = 30 * time.Second

// Hello is what the app shows of the daemon: its version and its agents.
type Hello struct {
	Version string
	Agents  []pagetools.AgentInfo
}

// MenuDeps is what the menu shows and what its choices do.
type MenuDeps struct {
	// Open makes the link, which calls onAction with each choice of the user.
	Open         func(onAction func(Action)) Link
	RegistryFile string
	Hello        func() Hello
	// Linked: whether the daemon is linked to the app at server now.
	Linked func(server string) bool
	// AppStatus is how a saved app stands, including why the daemon dropped it. Nil: a linked app is connected and any
	// other saved app is on its way to being linked.
	AppStatus func(server string) AppState
	// Sessions is the agents running for apps and for web pages.
	Sessions func() int
	// GatewayPort is the port of the local gateway, 0 when none was free.
	GatewayPort int
	Restart     func()
	Disconnect  func(origin string) error
	// Acp is the address of the standard ACP endpoint with its token, asked for at each refresh; false while there is
	// no endpoint or no token. Nil: the daemon has no such endpoint.
	Acp func() (AcpState, bool)
	Log *proc.Logger

	// Optional: Refresh defaults to two seconds, Now (the clock that tells which page grants have lapsed) to time.Now.
	Refresh time.Duration
	Now     func() time.Time
}

// notice is one sentence about a choice that failed, shown until it is read or a later choice works.
type notice struct {
	mu    sync.Mutex
	text  string
	until time.Time
}

func (n *notice) set(text string, now time.Time) {
	n.mu.Lock()
	n.text, n.until = text, now.Add(NoticeFor)
	n.mu.Unlock()
}

func (n *notice) clear() {
	n.mu.Lock()
	n.text = ""
	n.mu.Unlock()
}

func (n *notice) current(now time.Time) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.text == "" || !now.Before(n.until) {
		return ""
	}
	return n.text
}

// oneLineOf is the first line of an error, trimmed, without a trailing stop, and short enough for one menu line.
// An empty error says that no reason was given.
func oneLineOf(err error) string {
	s := ""
	if err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".")
	s = strings.TrimSpace(s)
	if s == "" {
		return "no reason was given"
	}
	if utf8.RuneCountInString(s) > 159 {
		return string([]rune(s)[:159]) + "…"
	}
	return s
}

// act does what the user chose; refresh sends the state again at once, for a choice that changes it. A choice that
// fails leaves one sentence for NoticeFor; one that works clears it.
func (d MenuDeps) act(action Action, refresh func(), note *notice, now func() time.Time) {
	switch action.Kind {
	case ActionRestart:
		d.Restart()
	case ActionDisconnect:
		d.Log.Say("disconnect " + action.Origin + " requested from the app")
		// Sent again whether it worked or not: the row goes at once, or stays because the registry still lists it.
		go func() {
			if err := d.Disconnect(action.Origin); err != nil {
				d.Log.Warn("app: cannot disconnect " + action.Origin + ": " + err.Error())
				note.set("Could not disconnect "+action.Origin+": "+oneLineOf(err)+".", now())
			} else {
				note.clear()
			}
			refresh()
		}()
	}
}

// StartMenu starts the link (agentd.app) and keeps it showing what the daemon serves. It returns what ends it.
func StartMenu(d MenuDeps) (end func()) {
	var link Link
	var note notice
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	status := d.AppStatus
	if status == nil {
		status = func(server string) AppState {
			if d.Linked(server) {
				return AppState{Status: AppConnected}
			}
			return AppState{Status: AppConnecting}
		}
	}
	push := func() {
		r, err := registry.Load(d.RegistryFile)
		if err != nil {
			return
		}
		hello := d.Hello()
		var saved []registry.App
		var apps, pages []string
		for _, a := range r.Apps {
			if !a.Live(now()) {
				continue
			}
			saved = append(saved, a)
			if d.Linked(a.Server) {
				apps = append(apps, a.Server)
			}
		}
		pageList := PageRows(r.Browsers, now())
		for _, b := range pageList {
			pages = append(pages, b.Origin)
		}
		var acp *AcpState
		if d.Acp != nil {
			if state, ok := d.Acp(); ok {
				acp = &state
			}
		}
		link.Update(NewState(Facts{
			Version:     hello.Version,
			Apps:        apps,
			AppList:     AppRows(saved, status),
			Notice:      note.current(now()),
			Browsers:    pages,
			PageList:    pageList,
			Sessions:    d.Sessions(),
			GatewayPort: d.GatewayPort,
			Agents:      hello.Agents,
			Acp:         acp,
		}))
	}
	link = d.Open(func(action Action) { d.act(action, push, &note, now) })
	link.Start()
	push()
	every := d.Refresh
	if every == 0 {
		every = refreshEvery
	}
	ticker := time.NewTicker(every)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-ticker.C:
				push()
			case <-done:
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
		<-finished
		link.Stop()
	}
}
