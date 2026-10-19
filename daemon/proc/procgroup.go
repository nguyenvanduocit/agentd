package proc

import "sync"

// Tracker remembers the process groups that must not outlive the daemon (an adapter install). The shell makes one
// and passes it to whoever starts such a group and to the way out of the process.
type Tracker struct {
	mu     sync.Mutex
	groups map[int]struct{}
}

func NewTracker() *Tracker {
	return &Tracker{groups: map[int]struct{}{}}
}

func (t *Tracker) Track(pgid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.groups[pgid] = struct{}{}
}

func (t *Tracker) Untrack(pgid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.groups, pgid)
}

// KillAll SIGKILLs every tracked group. The daemon calls it on every way out, including a fatal error.
func (t *Tracker) KillAll() {
	t.mu.Lock()
	groups := t.groups
	t.groups = map[int]struct{}{}
	t.mu.Unlock()
	for pgid := range groups {
		SignalGroup(pgid, true)
	}
}
