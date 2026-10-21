package proc

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Logger is the daemon's whole output: status lines on stdout, problems on stderr. The shell makes one and passes it
// to whoever has something to say.
type Logger struct {
	out     io.Writer
	err     io.Writer
	now     func() time.Time
	exit    func(code int)
	stamped atomic.Bool
	mu      sync.Mutex
}

func NewLogger(out, err io.Writer) *Logger {
	return &Logger{out: out, err: err, now: time.Now, exit: os.Exit}
}

// StampLines makes every line from now on start with a UTC timestamp: what a log file needs, and a terminal does not.
func (l *Logger) StampLines() {
	l.stamped.Store(true)
}

func (l *Logger) line(message string) string {
	if l.stamped.Load() {
		return l.now().UTC().Format("2006-01-02T15:04:05.000Z") + " " + message + "\n"
	}
	return message + "\n"
}

func (l *Logger) write(w io.Writer, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(w, l.line(message)) // stderr is gone: nothing else to tell
}

func (l *Logger) Say(message string) {
	l.write(l.out, message)
}

func (l *Logger) Warn(message string) {
	l.write(l.err, message)
}

// CrashOnPanic is deferred first thing in main and in every goroutine the daemon starts. A panic nobody recovered
// leaves the daemon in an unknown state: it logs the panic with its stack and exits 1. The groups in tracker (an adapter install running in the background) go
// down with the daemon.
func (l *Logger) CrashOnPanic(tracker *Tracker) {
	r := recover()
	if r == nil {
		return
	}
	l.Warn(fmt.Sprintf("fatal: uncaught exception: %v\n%s", r, debug.Stack()))
	tracker.KillAll()
	l.exit(1)
}
