package run

import (
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

const sleepAgent = "sh -c 'sleep 60'"

// startMulti starts n fake apps and one daemon paired with all of them (apps[i] saved with tokens[i], the test token
// when tokens is nil).
func startMulti(t *testing.T, n int, tokens []string) ([]*fakeApp, *daemonProc) {
	t.Helper()
	box := newSandbox(t)
	apps := make([]*fakeApp, n)
	saved := make([]registry.App, n)
	for i := range apps {
		apps[i] = startApp(t)
		saved[i] = registry.App{Server: apps[i].origin(), Token: testToken}
		if tokens != nil {
			saved[i].Token = tokens[i]
		}
	}
	box.writeApps(t, saved, nil)
	return apps, startDaemon(t, box, nil, map[string]string{"AGENTD_AGENT_CLAUDE": sleepAgent})
}

func TestEveryAppGetsHelloAndASessionIDMeansOneAgentPerApp(t *testing.T) {
	apps, _ := startMulti(t, 2, nil)
	a, b := apps[0], apps[1]
	a.waitHellos(t, 1)
	b.waitHellos(t, 1)
	pa := a.openOK(t, "s1", "claude")
	pb := b.openOK(t, "s1", "claude")
	if pa.Pid == pb.Pid {
		t.Fatalf("both apps' s1 run in the group %v", pa.Pid)
	}
	if !groupAlive(int(pa.Pid)) || !groupAlive(int(pb.Pid)) {
		t.Fatal("an agent is not running")
	}
	a.write(t, "s1", `{"a":1}`)
	for _, f := range b.framesOf("s1") {
		if f.Data == `{"a":1}` {
			t.Fatal("app b got what was written to app a's session")
		}
	}
}

func TestStopFromOneAppDetachesItAndTheOtherKeepsServing(t *testing.T) {
	apps, d := startMulti(t, 2, nil)
	a, b := apps[0], apps[1]
	a.waitHellos(t, 1)
	b.waitHellos(t, 1)
	a.stopDaemon(t)
	waitFor(t, "a is disconnected", func() bool { return !a.connected() })
	if d.hasExited() {
		t.Fatalf("the daemon ended with an app left; output:\n%s", d.out)
	}
	if !b.connected() {
		t.Fatal("b is no longer connected")
	}
	pb := b.openOK(t, "s2", "claude")
	if !groupAlive(int(pb.Pid)) {
		t.Fatal("b's agent is not running")
	}
}

func TestStopFromTheLastAppExits0(t *testing.T) {
	apps, d := startMulti(t, 1, nil)
	apps[0].waitHellos(t, 1)
	apps[0].stopDaemon(t)
	if code := d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
}

func TestARefusedTokenOnOneAppLeavesTheOtherServing(t *testing.T) {
	apps, d := startMulti(t, 2, []string{"wrong", testToken})
	a, b := apps[0], apps[1]
	b.waitHellos(t, 1)
	waitFor(t, "a rejected the token", func() bool { return a.rejected() >= 1 })
	if d.hasExited() {
		t.Fatalf("the daemon ended; output:\n%s", d.out)
	}
	if !b.connected() {
		t.Fatal("b is no longer connected")
	}
}

func TestADaemonWithNothingPairedStaysUpAndSaysWhereItServes(t *testing.T) {
	box := newSandbox(t)
	d := startDaemon(t, box, nil, nil)
	waitFor(t, "the serving line", func() bool { return d.says("serving agents") })
	if !d.says("for 0 app(s)") {
		t.Fatalf("output:\n%s", d.out)
	}
	waitForNothing(t, "the daemon to stay up", func() bool { return d.hasExited() })
}
