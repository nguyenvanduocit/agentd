package tray

import (
	"bufio"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const (
	// A tray that ends sooner than this after it started did not get to run (no display, no notification area).
	quickWindow   = 10 * time.Second
	maxQuickExits = 3
	restartDelay  = 2 * time.Second
	endGrace      = time.Second
)

// Tray is the daemon's icon in the menu bar or notification area: a program of its own (tray/), started with the
// daemon. It reads state lines on stdin and prints the entry the user chose on stdout; it ends when its stdin does, so
// a daemon that is killed takes it along. One that keeps ending at once is given up: the daemon runs without an icon.
type Tray struct {
	file     string
	onAction func(Action)
	log      *proc.Logger

	// The timings; tests shorten them.
	quickWindow, restartDelay, endGrace time.Duration

	mu         sync.Mutex
	child      *trayProc
	last       string
	quickExits int
	stopped    bool
	timer      *time.Timer
}

type trayProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// NewTray makes the tray of the program at file; Start runs it.
func NewTray(file string, onAction func(Action), log *proc.Logger) *Tray {
	return &Tray{file: file, onAction: onAction, log: log, quickWindow: quickWindow, restartDelay: restartDelay, endGrace: endGrace}
}

// Start runs the tray program.
func (t *Tray) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.start()
}

// start runs the program; the caller holds mu.
func (t *Tray) start() {
	if t.stopped {
		return
	}
	began := time.Now()
	// `child`: the tray program is also agentd.app, which runs on its own when started without it.
	cmd := exec.Command(t.file, "child")
	hideWindow(cmd)
	stdin, errIn := cmd.StdinPipe()
	stdout, errOut := cmd.StdoutPipe()
	stderr, errErr := cmd.StderrPipe()
	if err := firstError(errIn, errOut, errErr); err != nil {
		t.failed(err, began)
		return
	}
	if err := cmd.Start(); err != nil {
		t.failed(err, began)
		return
	}
	child := &trayProc{cmd: cmd, stdin: stdin}
	t.child = child
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		t.readChoices(stdout)
	}()
	go func() {
		defer readers.Done()
		t.relayStderr(stderr)
	}()
	go func() {
		readers.Wait()
		_ = cmd.Wait()
		t.mu.Lock()
		defer t.mu.Unlock()
		t.ended(child, began)
	}()
	t.send()
}

// failed is a program that could not start: like one that ended at once, it is tried again a few times.
func (t *Tray) failed(err error, began time.Time) {
	t.log.Warn("tray: cannot start " + t.file + ": " + err.Error())
	t.child = nil
	t.ended(nil, began)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *Tray) readChoices(r io.Reader) {
	lines := proc.NewLineSplitter(proc.MaxLineBytes)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		for _, line := range lines.Push(buf[:n]) {
			if action, ok := ParseLine(line); ok {
				t.onAction(action)
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *Tray) relayStderr(r io.Reader) {
	in := bufio.NewScanner(r)
	for in.Scan() {
		if text := strings.TrimSpace(in.Text()); text != "" {
			t.log.Say("tray: " + text)
		}
	}
}

// Update is the state to show now; sent again to a tray that was started anew. Nothing is sent when it did not change.
func (t *Tray) Update(state State) {
	text := state.line()
	t.mu.Lock()
	defer t.mu.Unlock()
	if text == t.last {
		return
	}
	t.last = text
	t.send()
}

// Stop ends the tray: its stdin closes, and it is killed if it does not follow.
func (t *Tray) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	if t.timer != nil {
		t.timer.Stop()
	}
	child := t.child
	if child == nil {
		return
	}
	_ = child.stdin.Close()
	time.AfterFunc(t.endGrace, func() {
		if err := child.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			_ = child.cmd.Process.Kill() // a platform without SIGTERM; for an ended process this fails and is nothing
		}
	})
}

// send writes the last state to the running program; the caller holds mu.
func (t *Tray) send() {
	if t.last == "" || t.child == nil {
		return
	}
	_, _ = io.WriteString(t.child.stdin, t.last+"\n") // EPIPE: it ended; ended says so
}

// ended is called when the program ended (child is nil when it could not start); the caller holds mu.
func (t *Tray) ended(child *trayProc, began time.Time) {
	if t.stopped || t.child != child {
		return
	}
	if time.Since(began) < t.quickWindow {
		t.quickExits++
	} else {
		t.quickExits = 0
	}
	if t.quickExits >= maxQuickExits {
		t.log.Warn("tray: giving up after " + strconv.Itoa(t.quickExits) + " quick exits (no display or notification area?); the daemon runs without an icon")
		return
	}
	t.timer = time.AfterFunc(t.restartDelay, t.Start)
}
