package proc

import (
	"fmt"
	"io"
	"io/fs"
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
// leaves the daemon in an unknown state: it logs the panic with its stack and exits 1, so the service manager (or the
// launcher's loop) starts a fresh process. The groups in tracker (an adapter install running in the background) go
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

const (
	LogMaxBytes int64 = 5 * 1024 * 1024
	LogKeep           = 3
)

// RotateLog rotates file when it is larger than maxBytes, keeping keep files in all; it returns whether it did. A
// missing file is not an error.
func RotateLog(file string, maxBytes int64, keep int) bool {
	info, err := os.Stat(file)
	if err != nil || !NeedsRotation(info.Size(), maxBytes) {
		return false
	}
	for _, step := range RotateSteps(file, keep) {
		// A missing older file is the normal case; a log must never stop the daemon.
		switch step.Op {
		case RotateRemove:
			_ = os.Remove(step.File)
		case RotateRename:
			_ = os.Rename(step.From, step.To)
		case RotateCopy:
			_ = copyFile(step.From, step.To)
		case RotateTruncate:
			_ = os.Truncate(step.File, 0)
		}
	}
	return true
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()&fs.ModePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// StdoutIs reports whether this process writes its stdout into file (a service manager or the launcher redirected it
// there).
func StdoutIs(file string) bool {
	out, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	log, err := os.Stat(file)
	if err != nil {
		return false
	}
	return os.SameFile(out, log)
}
