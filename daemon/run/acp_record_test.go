package run

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// logLines is where the log of a daemon of these tests goes.
type logLines struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(p)
}

func (l *logLines) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.text.String(), sub)
}

// acpDaemon is a daemon as far as the record of the endpoint and the token are concerned: a home, a port, a log.
func acpDaemon(t *testing.T, home string, port int) (*daemon, *logLines) {
	t.Helper()
	log := &logLines{}
	d := &daemon{Deps: Deps{Log: proc.NewLogger(log, log)}}
	d.acp0.home = home
	d.acp0.port.Store(int32(port))
	return d, log
}

func claudeOnly() []string { return []string{"claude"} }

func recordedAddress(t *testing.T, home string) string {
	t.Helper()
	info, err := acpauth.ReadInfo(home)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	return info.Address
}

// Two daemons can run on one home (agentd.app beside a terminal `agentd run`; nothing prevents it). The one that ends
// takes away its own record, never the other's.
func TestADaemonThatEndsTakesAwayItsOwnRecordAndNotTheOthers(t *testing.T) {
	home := t.TempDir()
	a, _ := acpDaemon(t, home, 47741)
	b, _ := acpDaemon(t, home, 47742)

	a.recordAcp(claudeOnly)
	b.recordAcp(claudeOnly) // the last to write is the one the record names
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:47742/acp" {
		t.Fatalf("the record names %s, want the last writer, 47742", got)
	}

	a.removeAcpInfo() // A ends: the record is B's
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:47742/acp" {
		t.Fatalf("after A ended the record names %s; B is still running and its record must stay", got)
	}
	b.removeAcpInfo()
	if got := recordedAddress(t, home); !strings.Contains(got, "no such file") {
		t.Errorf("after B ended the record is %s; want none", got)
	}
}

// The survivor puts its record back when the other one took it away (it ended while the record was its own), and does
// not fight over a record that is there.
func TestTheSurvivorRestoresTheRecordTheOtherDaemonTookAway(t *testing.T) {
	home := t.TempDir()
	a, _ := acpDaemon(t, home, 47741)
	b, _ := acpDaemon(t, home, 47742)
	a.recordAcp(claudeOnly)
	b.recordAcp(claudeOnly)
	b.removeAcpInfo() // B ends while the record was B's: nothing is recorded though A runs

	a.healAcp(claudeOnly)
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:47741/acp" {
		t.Fatalf("the survivor's record is %s, want 47741", got)
	}

	c, _ := acpDaemon(t, home, 47743)
	c.recordAcp(claudeOnly)
	a.healAcp(claudeOnly) // a record is there, another daemon's: not A's to overwrite on a timer
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:47743/acp" {
		t.Errorf("healing overwrote a record that was there: %s", got)
	}
}

// The record is judged by the file, not by what this daemon last wrote: a file that went away or that names another
// port is put right the next time the daemon records.
func TestTheRecordIsPutRightWhenTheFileWentAwayOrNamesAnotherPort(t *testing.T) {
	home := t.TempDir()
	d, _ := acpDaemon(t, home, 47741)
	d.recordAcp(claudeOnly)

	if err := os.Remove(acpauth.InfoFile(home)); err != nil {
		t.Fatal(err)
	}
	d.recordAcp(claudeOnly)
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:47741/acp" {
		t.Errorf("after the file went away the record is %s", got)
	}

	if err := acpauth.WriteInfo(home, acpauth.Info{Address: "ws://127.0.0.1:1/acp", Agents: claudeOnly()}); err != nil {
		t.Fatal(err)
	}
	d.recordAcp(claudeOnly)
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:47741/acp" {
		t.Errorf("a record that named another port is still %s", got)
	}

	d.recordAcp(func() []string { return []string{"claude", "codex"} })
	if info, _ := acpauth.ReadInfo(home); strings.Join(info.Agents, ",") != "claude,codex" {
		t.Errorf("a change of the ready agents is not recorded: %v", info.Agents)
	}
}

// Nothing is written when the file already says it.
func TestNothingIsWrittenWhenTheFileAlreadySaysIt(t *testing.T) {
	home := t.TempDir()
	d, _ := acpDaemon(t, home, 47741)
	if err := acpauth.WriteInfo(home, acpauth.Info{Address: "ws://127.0.0.1:47741/acp", Agents: claudeOnly(), Updated: "2000-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	d.recordAcp(claudeOnly)
	d.healAcp(claudeOnly)
	if info, _ := acpauth.ReadInfo(home); info.Updated != "2000-01-01T00:00:00Z" {
		t.Errorf("the record was rewritten although it was right: %+v", info)
	}
}

// A daemon without a gateway has no endpoint, and writes and removes nothing.
func TestADaemonWithoutAGatewayRecordsAndRemovesNothing(t *testing.T) {
	home := t.TempDir()
	d, _ := acpDaemon(t, home, 0)
	d.recordAcp(claudeOnly)
	d.healAcp(claudeOnly)
	if _, err := os.Stat(acpauth.InfoFile(home)); err == nil {
		t.Error("a record was written with no endpoint")
	}
	if err := acpauth.WriteInfo(home, acpauth.Info{Address: "ws://127.0.0.1:9/acp"}); err != nil {
		t.Fatal(err)
	}
	d.removeAcpInfo()
	if got := recordedAddress(t, home); got != "ws://127.0.0.1:9/acp" {
		t.Errorf("a daemon with no endpoint removed a record: %s", got)
	}
}

// After the daemon ended, a heal that was already running must not write the record back.
func TestAHealAfterTheEndWritesNothing(t *testing.T) {
	home := t.TempDir()
	d, _ := acpDaemon(t, home, 47741)
	d.recordAcp(claudeOnly)
	d.removeAcpInfo()
	d.healAcp(claudeOnly)
	d.recordAcp(claudeOnly)
	if _, err := os.Stat(acpauth.InfoFile(home)); err == nil {
		t.Error("the record came back after the daemon ended")
	}
}

// A problem with the token is told once; it is told again when it comes back after the token was fine.
func TestAProblemWithTheTokenIsToldAgainWhenItComesBackAfterARecovery(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(notADir, "home") // a home under a file: nothing can be made there
	good := filepath.Join(dir, "good")
	d, log := acpDaemon(t, broken, 47741)
	const line = "programs on this computer cannot use the standard ACP endpoint"

	for i := 0; i < 3; i++ {
		if got := d.acpToken(); got != "" {
			t.Fatalf("a token from a home that cannot hold one: %q", got)
		}
	}
	if n := log.count(line); n != 1 {
		t.Fatalf("the same problem was told %d times, want once", n)
	}

	d.acp0.home = good
	token := d.acpToken()
	if !acpauth.Valid(token) {
		t.Fatalf("after the home was fixed acpToken() = %q", token)
	}

	d.acp0.home = broken
	if got := d.acpToken(); got != "" {
		t.Fatalf("a token from a home that cannot hold one: %q", got)
	}
	if n := log.count(line); n != 2 {
		t.Errorf("the problem that came back after a recovery was told %d times in all, want 2", n)
	}
}

// A daemon that does not know its home yet touches nothing: an empty home would be a directory named acp in whatever
// the working directory is.
func TestADaemonWithNoHomeTouchesNothing(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	d, _ := acpDaemon(t, "", 47741)
	d.recordAcp(claudeOnly)
	d.healAcp(claudeOnly)
	d.removeAcpInfo()
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("a daemon with no home made %v in the working directory", entries)
	}
}
