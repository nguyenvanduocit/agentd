package home

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// One agentd home, one daemon. Two daemons on a home hold the same apps' tokens, and an app's server replaces a daemon's
// first socket when the same daemon connects again, so the two would flap against each other for ever and every agent
// would run twice. The daemon that starts second finds the lock held, says so, and stops.
//
// The lock is an operating-system lock on a file (flock; a share-nothing open on Windows), not a pid file: the system
// releases it when the process ends however it ends, a kill -9 included, so there is no stale lock to recognise. The
// file also holds a small record of who has the lock; it is read to word the message of the one that stays out, and
// never decides anything.
//
// The one that stays out exits with ExitStopped (0), on purpose. The exit codes are the daemon's protocol with whatever
// restarts it (exit.go): 0 stays stopped, so launchd (KeepAlive SuccessfulExit=false), systemd (Restart=on-failure) and
// the launcher's loop all leave it alone. Any other code would be started again at once, and the launcher counts a quick
// non-zero exit as a crash: after two it rolls a good version back.

// What started the daemon that holds the lock, as it is written in the record.
const (
	KindApp      = "app"      // agentd.app: it runs the daemon as its child and talks to it over a channel
	KindService  = "service"  // a launchd job, a systemd unit or the restart loop (AGENTD_MANAGED=1 without a channel)
	KindTerminal = "terminal" // `agentd run` by hand
)

// InstanceKind is what started a daemon: the app, when it has the channel to the app (the app sets AGENTD_MANAGED too),
// a service when a service manager restarts it, else a person at a terminal.
func InstanceKind(managed, hasUIChannel bool) string {
	switch {
	case hasUIChannel:
		return KindApp
	case managed:
		return KindService
	}
	return KindTerminal
}

// LockRecord is what the daemon that holds the lock writes in the lock file.
type LockRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Kind      string    `json:"kind"`
}

// HeldError is returned when another process holds the lock. Holder is its record, the zero value when it could not be
// read.
type HeldError struct{ Holder LockRecord }

func (e *HeldError) Error() string { return "another agentd holds this home's lock" }

// Message is the one line the daemon that stays out says.
func (e *HeldError) Message(now time.Time) string {
	text := "agentd is already running on this computer"
	if who := e.Holder.describe(now); who != "" {
		text += " (" + who + ")"
	}
	return text + ": not starting a second one. Use that one, or quit it first."
}

// describe is "pid 4242, started by the app, 3 minutes ago", leaving out what the record does not say.
func (r LockRecord) describe(now time.Time) string {
	if r.PID == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("pid %d", r.PID)}
	switch r.Kind {
	case KindApp:
		parts = append(parts, "started by the app")
	case KindService:
		parts = append(parts, "started as a service")
	case KindTerminal:
		parts = append(parts, "started from a terminal")
	}
	if !r.StartedAt.IsZero() {
		parts = append(parts, ago(now.Sub(r.StartedAt)))
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

func ago(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s ago", unit)
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return plural(int(d/time.Second), "second")
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

// InstanceLock is the lock of one home, held until Close or until the process ends.
type InstanceLock struct {
	f *os.File
}

// Close releases the lock. It is safe to call more than once.
func (l *InstanceLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close()
}

// Record writes who holds the lock into its file. Call it only once the lock is held: a process that stays out must
// leave the holder's record as it is.
func (l *InstanceLock) Record(r LockRecord) error {
	if l == nil || l.f == nil {
		return nil
	}
	text, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err = l.f.WriteAt(append(text, '\n'), 0)
	return err
}

// readHolder is the record in an open lock file, the zero value when there is none or it is not one.
func readHolder(f *os.File) LockRecord {
	buf := make([]byte, 4096)
	n, _ := f.ReadAt(buf, 0)
	var r LockRecord
	if json.Unmarshal(buf[:n], &r) != nil {
		return LockRecord{}
	}
	return r
}
