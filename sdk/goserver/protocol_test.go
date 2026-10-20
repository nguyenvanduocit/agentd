package goserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// helloOf is the hello of a daemon on the laptop that names the protocols min-max (none: a daemon older than the range).
func helloOf(min, max float64) contract.Hello {
	return contract.Hello{Version: "0.7.0", Hostname: "laptop", Os: "darwin", Arch: "arm64", Agents: allAgents(), ProtocolMin: min, ProtocolMax: max}
}

// pairedAndConnected pairs user's daemon and connects it, without a hello.
func (e env) pairedAndConnected(t *testing.T, user string) *fakeDaemon {
	t.Helper()
	token := e.pairHost(t, user, "laptop")
	d := connectDaemon(t, e.srv.URL, token)
	d.id, d.token = e.idOf(t, token), token
	return d
}

func TestADaemonWhoseRangeHoldsTheAppsProtocolComesOnline(t *testing.T) {
	e := newEnv(t)
	d := e.pairedAndConnected(t, "alice")
	d.hello(helloOf(goserver.Protocol, goserver.Protocol+2))
	info := waitDaemon(t, e.server, "alice", d.id, "online")
	if info.Incompatible != "" {
		t.Errorf("Incompatible = %q on an online daemon", info.Incompatible)
	}
}

// A daemon older than the range sends no protocolMin or protocolMax: it speaks the one protocol it was built with.
func TestADaemonThatNamesNoRangeIsTakenForProtocolOne(t *testing.T) {
	e := newEnv(t)
	d := e.pairedAndConnected(t, "alice")
	d.hello(helloOf(0, 0))
	waitDaemon(t, e.server, "alice", d.id, "online")
}

func TestADaemonThatSpeaksOnlyNewerProtocolsIsNotOnlineAndTheListSaysWhy(t *testing.T) {
	e := newEnv(t)
	d := e.pairedAndConnected(t, "alice")
	newer := float64(goserver.Protocol + 1)
	d.hello(helloOf(newer, newer+1))

	info := waitDaemon(t, e.server, "alice", d.id, "offline")
	waitIncompatible(t, e, "alice", d.id)
	info, _ = infoOf(e.list(t, "alice"), d.id)
	if !strings.Contains(info.Incompatible, "needs an update") || info.Version != "0.7.0" || info.Hostname != "laptop" {
		t.Errorf("entry = %+v, want the reason and the version and host the daemon reported", info)
	}
	if got := e.server.Online("alice"); len(got) != 0 {
		t.Errorf("online = %v, want none", got)
	}
	_, err := e.server.Open(context.Background(), "alice", d.id, "claude")
	var incompatible goserver.IncompatibleError
	if !errors.As(err, &incompatible) || incompatible.Reason != info.Incompatible || !strings.Contains(err.Error(), info.Incompatible) {
		t.Errorf("Open = %v, want an IncompatibleError that carries the reason", err)
	}
	if errors.As(err, new(goserver.OfflineError)) {
		t.Errorf("Open = %v: an incompatible daemon is not just offline", err)
	}
}

func TestADaemonThatTopsOutBelowTheAppsProtocolIsToldToUpdate(t *testing.T) {
	// goserver.Protocol is 1 and a daemon cannot name a range below 1, so the older daemon is the pure function's
	// case (protocol_internal_test.go); here the daemon only has to be refused with the reason of its side.
	e := newEnv(t)
	d := e.pairedAndConnected(t, "alice")
	d.hello(helloOf(goserver.Protocol+1, goserver.Protocol+1))
	waitIncompatible(t, e, "alice", d.id)
	info, _ := infoOf(e.list(t, "alice"), d.id)
	if strings.Contains(info.Incompatible, "Update agentd") {
		t.Errorf("a daemon newer than the app was told to update itself: %q", info.Incompatible)
	}
}

func TestTheReasonOutlivesTheConnectionAndGoesWhenTheDaemonComesBackInRange(t *testing.T) {
	e := newEnv(t)
	d := e.pairedAndConnected(t, "alice")
	d.hello(helloOf(goserver.Protocol+1, goserver.Protocol+1))
	waitIncompatible(t, e, "alice", d.id)

	d.sock.Close() // the daemon leaves the app, as a daemon that speaks other protocols does
	time.Sleep(200 * time.Millisecond)
	if info, _ := infoOf(e.list(t, "alice"), d.id); info.Incompatible == "" {
		t.Error("the reason went with the connection: the list says only that the daemon is offline")
	}

	d.redial()
	d.hello(helloOf(goserver.Protocol, goserver.Protocol))
	info := waitDaemon(t, e.server, "alice", d.id, "online")
	if info.Incompatible != "" {
		t.Errorf("Incompatible = %q after the daemon came back in range", info.Incompatible)
	}
}

func TestIncompatibleIsLeftOutOfTheJSONOfADaemonThatIsFine(t *testing.T) {
	out, err := json.Marshal(goserver.Info{ID: "d1"})
	if err != nil || strings.Contains(string(out), "incompatible") {
		t.Errorf("Info = %s (%v): a field that is empty must not change what apps already read", out, err)
	}
	out, _ = json.Marshal(goserver.Info{ID: "d1", Incompatible: "update agentd"})
	if !strings.Contains(string(out), `"incompatible":"update agentd"`) {
		t.Errorf("Info = %s", out)
	}
}

func TestRevokingADaemonForgetsWhyItWasRefused(t *testing.T) {
	e := newEnv(t)
	d := e.pairedAndConnected(t, "alice")
	d.hello(helloOf(goserver.Protocol+1, goserver.Protocol+1))
	waitIncompatible(t, e, "alice", d.id)
	if _, err := e.server.Revoke("alice", d.id); err != nil {
		t.Fatal(err)
	}
	if _, known := infoOf(e.list(t, "alice"), d.id); known {
		t.Error("a revoked daemon is still listed")
	}
	if _, err := e.server.Ready("alice", d.id); !errors.Is(err, goserver.ErrUnknownDaemon) {
		t.Errorf("Ready = %v, want ErrUnknownDaemon", err)
	}
}

// waitIncompatible waits until user's list gives a reason for daemon id.
func waitIncompatible(t *testing.T, e env, user, id string) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if info, ok := infoOf(e.list(t, user), id); ok && info.Incompatible != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the list never gave a reason for daemon %s: %+v", id, e.list(t, user))
}
