package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/contract"
)

func TestHelloSaysVersionMachineAndAgents(t *testing.T) {
	r := startRig(t, sh("sleep 60"), nil)
	hello := r.app.firstHello(t)
	if hello.Version != testVersion || hello.Os != nodeOS() || hello.Arch != nodeArch() || hello.Hostname == "" {
		t.Fatalf("hello = %+v", hello)
	}
	if hello.Managed {
		t.Fatal("a daemon that no service restarts says managed")
	}
	var names []string
	for _, a := range hello.Agents {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "claude,codex,grok" {
		t.Fatalf("agents = %v", names)
	}
	if want := (contract.AgentInfo{Name: "claude", Title: "Claude Code", Ready: true, Reason: ""}); hello.Agents[0] != want {
		t.Fatalf("claude = %+v, want %+v", hello.Agents[0], want)
	}
	if got := r.app.accepted(); got != 1 {
		t.Fatalf("accepted sockets = %d, want 1", got)
	}
	waitFor(t, "the status line", func() bool { return r.d.says("connected to " + r.app.origin()) })
}

func TestMachineWithoutNpmAndGrokReportsWhy(t *testing.T) {
	empty := t.TempDir()
	r := startRig(t, "", map[string]string{"PATH": empty, "AGENTD_AGENT_CODEX": "", "AGENTD_AGENT_CLAUDE": unset})
	agents := r.app.firstHello(t).Agents
	if len(agents) != 3 || agents[0].Name != "claude" || agents[1].Name != "codex" || agents[2].Name != "grok" {
		t.Fatalf("agents = %+v", agents)
	}
	for _, a := range agents[:2] {
		title := map[string]string{"claude": "Claude", "codex": "Codex"}[a.Name]
		want := regexp.MustCompile(`^(installing the ` + title + ` adapter|cannot install the ` + title + ` adapter: npm not found: install Node\.js \(it comes with npm\); retrying in \d+ s)`)
		if a.Ready || !want.MatchString(a.Reason) {
			t.Fatalf("%s = %+v, want a reason that matches %s", a.Name, a, want)
		}
	}
	if want := (contract.AgentInfo{Name: "grok", Title: "Grok", Ready: false, Reason: "grok not found in PATH"}); agents[2] != want {
		t.Fatalf("grok = %+v, want %+v", agents[2], want)
	}
	if _, err := r.app.open(t, "s1", "grok"); err == nil || !strings.Contains(err.Error(), "agent grok is not ready: grok not found in PATH") {
		t.Fatalf("open grok: %v", err)
	}
}

func TestOpenRefusals(t *testing.T) {
	r := startRig(t, sh("sleep 60"), nil)
	for _, c := range []struct{ session, agent, want string }{
		{"s1", "rm -rf", `unknown agent "rm -rf"`},
		{"", "claude", "empty session id"},
	} {
		if _, err := r.app.open(t, c.session, c.agent); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("open(%q, %q) = %v, want %q", c.session, c.agent, err, c.want)
		}
	}
	r.app.openOK(t, "s2", "claude")
	if _, err := r.app.open(t, "s2", "claude"); err == nil || !strings.Contains(err.Error(), "session s2 exists") {
		t.Fatalf("a repeated session id: %v", err)
	}
	r.app.closeSession(t, "s2")
	r.app.frameWhere(t, "s2", "the exit of s2", isKind("exit"))
	r.app.openOK(t, "s2", "claude") // the id is free again
}

func TestOpenCommandThatDoesNotExist(t *testing.T) {
	r := startRig(t, "/nonexistent/agent", nil)
	if _, err := r.app.open(t, "s1", "claude"); err == nil || !strings.Contains(err.Error(), `command "/nonexistent/agent" not found`) {
		t.Fatalf("open: %v", err)
	}
	if n := len(r.app.framesOf("s1")); n != 0 {
		t.Fatalf("%d frames of a session that never started", n)
	}
}

func TestWorkingDirectoryIsWhereTheDaemonStartedAndDirOverrides(t *testing.T) {
	r := startRig(t, sh("pwd; sleep 60"), nil)
	opened := r.app.openOK(t, "s1", "claude")
	if opened.Cwd != r.box.work {
		t.Fatalf("cwd = %q, want %q", opened.Cwd, r.box.work)
	}
	first := r.app.frameWhere(t, "s1", "pwd", isKind("out"))
	if first.Data != r.box.work {
		t.Fatalf("pwd = %q, want %q", first.Data, r.box.work)
	}

	other := t.TempDir()
	r.d.stop()
	r.start(t, sh("pwd; sleep 60"), nil, "--dir", other)
	r.app.waitHellos(t, 2)
	o2 := r.app.openOK(t, "s2", "claude")
	if o2.Cwd != other {
		t.Fatalf("cwd = %q, want %q", o2.Cwd, other)
	}
	line := r.app.frameWhere(t, "s2", "pwd of s2", isKind("out"))
	got, _ := filepath.EvalSymlinks(line.Data)
	want, _ := filepath.EvalSymlinks(other)
	if got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

func TestDirThatIsNotADirectoryStopsTheDaemonWithAMessage(t *testing.T) {
	box := newSandbox(t)
	d := startDaemon(t, box, []string{"--dir", "/definitely/not/here"}, nil)
	if code := d.exitCode(t, waitLimit); code != 1 {
		t.Fatalf("exit code %d, want 1; output:\n%s", code, d.out)
	}
	if !d.says("is not a directory") {
		t.Fatalf("output:\n%s", d.out)
	}
}

func TestStdoutLinesArriveInOrderAndTheExitFrameIsLast(t *testing.T) {
	r := startRig(t, sh("i=0; while [ $i -lt 3000 ]; do echo line$i; i=$((i+1)); done"), nil)
	r.app.openOK(t, "s1", "claude")
	exit := r.app.frameWhere(t, "s1", "the exit frame", isKind("exit"))
	frames := r.app.framesOf("s1")
	if frames[len(frames)-1] != exit || exit.Code != 0 {
		t.Fatalf("the last frame is %+v, the exit %+v", frames[len(frames)-1], exit)
	}
	var lines []string
	exits := 0
	for _, f := range frames {
		switch f.Kind {
		case "out":
			lines = append(lines, f.Data)
		case "exit":
			exits++
		}
	}
	if len(lines) != 3000 || exits != 1 {
		t.Fatalf("%d lines and %d exit frames", len(lines), exits)
	}
	for i, l := range lines {
		if l != fmt.Sprintf("line%d", i) {
			t.Fatalf("line %d is %q", i, l)
		}
	}
}

func TestStderrArrivesAsErrFramesAndTheExitCodeIsTheAgents(t *testing.T) {
	r := startRig(t, sh("echo oops >&2; exit 7"), nil)
	r.app.openOK(t, "s1", "claude")
	exit := r.app.frameWhere(t, "s1", "the exit frame", isKind("exit"))
	if exit.Code != 7 {
		t.Fatalf("exit code %d, want 7", exit.Code)
	}
	var errs []string
	for _, f := range r.app.framesOf("s1") {
		if f.Kind == "err" {
			errs = append(errs, f.Data)
		}
	}
	if strings.Join(errs, "") != "oops\n" {
		t.Fatalf("err frames = %q", errs)
	}
}

func TestACPLinesGoThroughTheFakeAgentBothWays(t *testing.T) {
	r := startRig(t, binaries.fakeAgent, nil)
	r.app.openOK(t, "s1", "claude")
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`
	if !r.app.write(t, "s1", init) {
		t.Fatal("the write was refused")
	}
	reply := r.app.frameWhere(t, "s1", "the initialize reply", isKind("out"))
	var got struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion int `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply.Data), &got); err != nil || got.ID != 1 || got.Result.ProtocolVersion != 1 {
		t.Fatalf("reply %q: %v", reply.Data, err)
	}
}

func TestWriteToASessionThatDoesNotExistIsFalseAndCloseOfOneIsSilent(t *testing.T) {
	r := startRig(t, "sleep 60", nil)
	if r.app.write(t, "nope", "x") {
		t.Fatal("a write to no session was taken")
	}
	r.app.closeSession(t, "nope")
}

func TestCloseEndsTheAgentWithItsWholeGroup(t *testing.T) {
	r := startRig(t, sh("sleep 300 & sleep 300 & wait"), nil)
	opened := r.app.openOK(t, "s1", "claude")
	if !groupAlive(int(opened.Pid)) {
		t.Fatal("the agent's group is not running")
	}
	r.app.closeSession(t, "s1")
	exit := r.app.frameWhere(t, "s1", "the exit frame", isKind("exit"))
	if exit.Code != 128 {
		t.Fatalf("exit code %d, want 128", exit.Code)
	}
	waitGroupGone(t, int(opened.Pid))
}

func TestAnAgentThatEndsLeavesNothingBehindInItsGroup(t *testing.T) {
	r := startRig(t, sh("sleep 300 & echo started"), nil)
	opened := r.app.openOK(t, "s1", "claude")
	exit := r.app.frameWhere(t, "s1", "the exit frame", isKind("exit"))
	if exit.Code != 0 {
		t.Fatalf("exit code %d, want 0", exit.Code)
	}
	waitFor(t, "the leftover sleep to be killed", func() bool { return !groupAlive(int(opened.Pid)) })
}

func TestTwelveMiBInEachDirectoryByteForByte(t *testing.T) {
	r := startRig(t, "cat", nil)
	r.app.openOK(t, "s1", "claude")
	const size = 12 * 1024 * 1024
	line := strings.Repeat("đ\U0001F600x", size/7+1)
	for len(line) < size {
		line += "x"
	}
	if !r.app.write(t, "s1", line) {
		t.Fatal("the write was refused")
	}
	var echoed frame
	waitForWithin(t, 60*time.Second, "the echoed line", func() bool {
		for _, f := range r.app.framesOf("s1") {
			if f.Kind == "out" {
				echoed = f
				return true
			}
		}
		return false
	})
	sum := sha256.Sum256([]byte(line))
	if echoed.Len != len(line) || echoed.SHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("echoed %d bytes (sha %s), sent %d (sha %x)", echoed.Len, echoed.SHA, len(line), sum)
	}
}

func TestSocketDrop(t *testing.T) {
	for _, c := range []struct {
		name      string
		transport bool
	}{{"the server ends the socket", false}, {"the connection breaks", true}} {
		t.Run(c.name, func(t *testing.T) {
			r := startRig(t, sh("sleep 300 & sleep 300 & echo up; wait"), nil)
			opened := r.app.openOK(t, "s1", "claude")
			r.app.frameWhere(t, "s1", "the first line", isKind("out"))
			if !groupAlive(int(opened.Pid)) {
				t.Fatal("the agent's group is not running")
			}

			r.app.kick(t, c.transport)

			waitFor(t, "the whole group to die", func() bool { return !groupAlive(int(opened.Pid)) })
			r.app.waitHellos(t, 2)
			waitFor(t, "the daemon to be connected again", r.app.connected)
			time.Sleep(500 * time.Millisecond)
			for _, f := range r.app.framesOf("s1") {
				if f.Kind == "exit" {
					t.Fatal("an exit frame arrived after the socket dropped: a stale frame")
				}
			}
			if got := r.app.accepted(); got != 2 {
				t.Fatalf("accepted sockets = %d, want 2", got)
			}
			if !r.d.says("disconnected from") {
				t.Fatalf("output:\n%s", r.d.out)
			}
			// the daemon serves new sessions after the reconnect
			again := r.app.openOK(t, "s2", "claude")
			if !groupAlive(int(again.Pid)) {
				t.Fatal("the new session's group is not running")
			}
		})
	}
}

func TestSIGTERMEndsAgentsAndDaemonAndRemovesTheWrapperDirectoryAndTheState(t *testing.T) {
	r := startRig(t, sh("sleep 300 & sleep 300 & wait"), nil)
	opened := r.app.openOK(t, "s1", "claude")
	state := readSessions(t, r.box.sessionsFile())
	if len(state.Groups) != 1 || state.Groups[0].Pgid != int(opened.Pid) {
		t.Fatalf("the state records %+v, want the group %d", state.Groups, int(opened.Pid))
	}
	if !regexp.MustCompile(`/agentd-[0-9A-Za-z]{6}/bin$`).MatchString(state.Bin) {
		t.Fatalf("bin = %q: not <tmp>/agentd-XXXXXX/bin", state.Bin)
	}
	r.d.signal(syscall.SIGTERM)
	if code := r.d.exitCode(t, waitLimit); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if groupAlive(int(opened.Pid)) {
		t.Fatal("the agent's group survived the daemon")
	}
	if _, err := os.Stat(r.box.sessionsFile()); !os.IsNotExist(err) {
		t.Fatalf("the state file is still there (%v)", err)
	}
	if _, err := os.Stat(filepath.Dir(state.Bin)); !os.IsNotExist(err) {
		t.Fatalf("the wrapper directory %s is still there (%v)", filepath.Dir(state.Bin), err)
	}
}

func TestSIGKILLLeavesOrphansAndTheNextStartEndsThem(t *testing.T) {
	r := startRig(t, sh("sleep 300 & sleep 300 & wait"), nil)
	opened := r.app.openOK(t, "s1", "claude")
	waitFor(t, "the leader's start time in the state file", func() bool {
		s := readSessions(t, r.box.sessionsFile())
		return len(s.Groups) > 0 && s.Groups[0].Lstart != ""
	})
	r.d.signal(syscall.SIGKILL)
	r.d.exitCode(t, waitLimit)
	if !groupAlive(int(opened.Pid)) {
		t.Fatal("the orphans are gone: the leader and two sleeps should live on")
	}

	r.start(t, sh("sleep 300"), nil)
	waitFor(t, "the orphans to be ended", func() bool { return !groupAlive(int(opened.Pid)) })
	r.app.waitHellos(t, 2)
	if !r.d.says("reaped 1 orphan group(s), 3 process(es)") {
		t.Fatalf("output:\n%s", r.d.out)
	}
}

type sessionsRecord struct {
	Bin    string `json:"bin"`
	Groups []struct {
		Pgid   int    `json:"pgid"`
		Lstart string `json:"lstart"`
	} `json:"groups"`
}

func readSessions(t *testing.T, file string) sessionsRecord {
	t.Helper()
	var s sessionsRecord
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
