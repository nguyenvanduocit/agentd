package run

import (
	"syscall"
	"testing"
	"time"
)

// Restart and Stop from the server, and a revoked token: the exit codes that tell the service what to do (0 stay
// stopped, 75 start again now). The daemon runs directly here; AGENTD_MANAGED=1 is what the launcher's loop, the
// service definitions and agentd.app set.

// tree is an agent with a child, so a lingering process shows.
var tree = sh("sleep 600 & echo up; wait")

func TestNotManagedRestartIsIgnoredAndStopExits0(t *testing.T) {
	r := startRig(t, tree, nil)
	if r.app.firstHello(t).Managed {
		t.Fatal("hello says managed")
	}
	pid := r.d.pid()
	r.app.restart(t)
	waitFor(t, "the log line", func() bool { return r.d.says("not managed: restart ignored") })
	time.Sleep(1500 * time.Millisecond)
	if r.d.hasExited() || r.d.pid() != pid {
		t.Fatalf("the daemon ended on a Restart it should have ignored; output:\n%s", r.d.out)
	}
	if !r.app.connected() {
		t.Fatal("the daemon is no longer connected")
	}

	r.app.stopDaemon(t)
	if code := r.d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !r.d.says("stop requested by the server") {
		t.Fatalf("output:\n%s", r.d.out)
	}
}

func TestStopInTheMiddleOfATurnEndsTheAgentsTreeAndTheExitFrameStillArrives(t *testing.T) {
	r := startRig(t, tree, nil)
	opened := r.app.openOK(t, "busy", "claude")
	r.app.frameWhere(t, "busy", "the agent's first line", isKind("out"))
	if !groupAlive(int(opened.Pid)) {
		t.Fatal("the agent's group is not running")
	}
	r.app.stopDaemon(t)
	if code := r.d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if groupAlive(int(opened.Pid)) {
		t.Fatal("the agent's group survived the daemon")
	}
	r.app.frameWhere(t, "busy", "the exit frame", isKind("exit"))
}

func TestManagedHelloSaysManagedAndRestartMidTurnExits75(t *testing.T) {
	r := startRig(t, tree, map[string]string{"AGENTD_MANAGED": "1"})
	if !r.app.firstHello(t).Managed {
		t.Fatal("hello says not managed")
	}
	opened := r.app.openOK(t, "busy", "claude")
	r.app.frameWhere(t, "busy", "the agent's first line", isKind("out"))
	r.app.restart(t)
	if code := r.d.exitCode(t, waitLimit); code != 75 {
		t.Fatalf("exit code %d, want 75; output:\n%s", code, r.d.out)
	}
	if groupAlive(int(opened.Pid)) {
		t.Fatal("the agent's group survived the restart")
	}
	r.app.frameWhere(t, "busy", "the exit frame", isKind("exit"))
	if !r.d.says("restart requested by the server") || !r.d.says("exiting 75") {
		t.Fatalf("output:\n%s", r.d.out)
	}
}

func TestManagedStopMidTurnExits0(t *testing.T) {
	r := startRig(t, tree, map[string]string{"AGENTD_MANAGED": "1"})
	opened := r.app.openOK(t, "busy", "claude")
	r.app.frameWhere(t, "busy", "the agent's first line", isKind("out"))
	r.app.stopDaemon(t)
	if code := r.d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if groupAlive(int(opened.Pid)) {
		t.Fatal("the agent's group survived the stop")
	}
}

func TestRevokedTokenExits0AndDoesNotRetry(t *testing.T) {
	r := startRig(t, tree, map[string]string{"AGENTD_MANAGED": "1"})
	r.app.revoke()
	if code := r.d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0; output:\n%s", code, r.d.out)
	}
	if got := r.app.rejected(); got != 1 {
		t.Fatalf("rejected handshakes = %d, want 1", got)
	}
	time.Sleep(time.Second)
	if got := r.app.rejected(); got != 1 {
		t.Fatalf("the daemon tried again: rejected = %d", got)
	}
	if !r.d.says("refused this daemon") {
		t.Fatalf("output:\n%s", r.d.out)
	}
}

func TestSIGINTStopsLikeSIGTERM(t *testing.T) {
	r := startRig(t, tree, nil)
	r.d.signal(syscall.SIGINT)
	if code := r.d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
}

func TestASecondSignalWhileTheDaemonShutsDownEndsItAtOnce(t *testing.T) {
	// An agent that ignores SIGTERM keeps the shutdown waiting for its grace period.
	r := startRig(t, sh(`trap "" TERM; echo up; while :; do sleep 1; done`), nil)
	opened := r.app.openOK(t, "stubborn", "claude")
	r.app.frameWhere(t, "stubborn", "the agent to have set its trap", isKind("out"))
	r.d.signal(syscall.SIGTERM)
	time.Sleep(300 * time.Millisecond)
	if r.d.hasExited() {
		t.Fatal("the shutdown ended before a second signal could arrive")
	}
	began := time.Now()
	r.d.signal(syscall.SIGTERM)
	r.d.exitCode(t, time.Second)
	if r.d.code != -1 {
		t.Fatalf("exit code %d, want -1 (ended by the signal)", r.d.code)
	}
	if time.Since(began) > time.Second {
		t.Fatalf("took %s", time.Since(began))
	}
	_ = syscall.Kill(-int(opened.Pid), syscall.SIGKILL)
}
