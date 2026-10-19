package home

import "testing"

func TestAgentdHome(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  Env
		want string
	}{
		{"~/.agentd", nil, "/home/ann/.agentd"},
		{"$AGENTD_HOME when set", Env{"AGENTD_HOME": "/x/mc"}, "/x/mc"},
		{"$AGENTD_HOME when empty does not count", Env{"AGENTD_HOME": ""}, "/home/ann/.agentd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentdHome(linux(tc.env)); got != tc.want {
				t.Errorf("AgentdHome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLayoutOfHangsEveryPathOffTheHome(t *testing.T) {
	l := LayoutOf("/h/.agentd")
	want := Layout{
		Home:         "/h/.agentd",
		NodeDir:      "/h/.agentd/node",
		DaemonDir:    "/h/.agentd/daemon",
		CurrentLink:  "/h/.agentd/daemon/current",
		PreviousFile: "/h/.agentd/daemon/previous",
		RejectedFile: "/h/.agentd/daemon/rejected",
		CrashesFile:  "/h/.agentd/daemon/crashes",
		Launcher:     "/h/.agentd/bin/agentd",
		LogFile:      "/h/.agentd/logs/daemon.log",
		LoopPidFile:  "/h/.agentd/run/loop.pid",
		AdaptersDir:  "/h/.agentd/adapters",
		LastExitFile: "/h/.agentd/run/last-exit",
	}
	if l != want {
		t.Errorf("LayoutOf = %+v, want %+v", l, want)
	}
	if got := VersionDir(l, "1.2.3"); got != "/h/.agentd/daemon/1.2.3" {
		t.Errorf("VersionDir = %q", got)
	}
}

func TestInstalledVersion(t *testing.T) {
	l := LayoutOf("/h/.agentd")
	for _, tc := range []struct {
		name, exe, want string
		ok              bool
	}{
		{"an agentd directly inside <home>/daemon/<version>", "/h/.agentd/daemon/0.0.0-g1a2b/agentd", "0.0.0-g1a2b", true},
		{"a go run build", "/tmp/go-build123/b001/exe/agentd-dev", "", false},
		{"a test build", "/tmp/build/agentd", "", false},
		{"another home", "/other/.agentd/daemon/1.0.0/agentd", "", false},
		{"another file name", "/h/.agentd/daemon/1.0.0/daemon.js", "", false},
		{"an agentd one level too deep", "/h/.agentd/daemon/1.0.0/sub/agentd", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := InstalledVersion(tc.exe, l)
			if got != tc.want || ok != tc.ok {
				t.Errorf("InstalledVersion(%q) = %q, %v; want %q, %v", tc.exe, got, ok, tc.want, tc.ok)
			}
		})
	}
}
