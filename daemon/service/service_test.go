package service

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

func TestNamesAndFiles(t *testing.T) {
	// the user's service has the fixed label and unit name; the seams rename and relocate it
	cases := []struct{ got, want string }{
		{ServiceName(Launchd, home.Env{}), "com.agentd.daemon"},
		{ServiceName(Systemd, home.Env{}), "agentd"},
		{ServiceName(Launchd, home.Env{"AGENTD_SERVICE_LABEL": "com.agentd.daemon.test.ab12"}), "com.agentd.daemon.test.ab12"},
		{ServiceFile(Launchd, "com.agentd.daemon", "/Users/ann", home.Env{}), "/Users/ann/Library/LaunchAgents/com.agentd.daemon.plist"},
		{ServiceFile(Systemd, "agentd", "/home/ann", home.Env{}), "/home/ann/.config/systemd/user/agentd.service"},
		{ServiceFile(Systemd, "agentd", "/home/ann", home.Env{"XDG_CONFIG_HOME": "/x"}), "/x/systemd/user/agentd.service"},
		{ServiceFile(Launchd, "t", "/Users/ann", home.Env{"AGENTD_SERVICE_DIR": "/tmp/d"}), "/tmp/d/t.plist"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestPassthroughEnv(t *testing.T) {
	t.Run("PATH, the XDG directories and AGENTD_* go into the service; the rest of the shell does not", func(t *testing.T) {
		got := PassthroughEnv(home.Env{
			"PATH":                 "/opt/homebrew/bin:/usr/bin",
			"HOME":                 "/Users/ann",
			"SECRET_TOKEN":         "x",
			"XDG_CONFIG_HOME":      "/c",
			"XDG_DATA_HOME":        "/d",
			"AGENTD_HOME":          "/mc",
			"AGENTD_AGENT_CLAUDE":  "/fake",
			"AGENTD_RESTART":       "loop",
			"AGENTD_MANAGED":       "0",
			"AGENTD_SERVICE_LABEL": "t",
			"AGENTD_SERVICE_DIR":   "/d",
			"EMPTY":                "",
		})
		want := map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin", "XDG_CONFIG_HOME": "/c", "AGENTD_HOME": "/mc", "AGENTD_AGENT_CLAUDE": "/fake"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("the consent seam never goes into the service: an installed daemon always asks the user", func(t *testing.T) {
		got := PassthroughEnv(home.Env{"PATH": "/usr/bin", "AGENTD_CONSENT": "/usr/bin/true"})
		if !reflect.DeepEqual(got, map[string]string{"PATH": "/usr/bin"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("the variables that name the user's screen go into the service: the tray finds the display with them", func(t *testing.T) {
		got := PassthroughEnv(home.Env{"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-0", "XAUTHORITY": "/home/ann/.Xauthority", "LANG": "C"})
		want := map[string]string{"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-0", "XAUTHORITY": "/home/ann/.Xauthority"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a value with a line break or a NUL stays out", func(t *testing.T) {
		got := PassthroughEnv(home.Env{"PATH": "/a\n/b", "AGENTD_X": "a\x00b", "AGENTD_Y": "a\rb", "AGENTD_OK": "1"})
		if !reflect.DeepEqual(got, map[string]string{"AGENTD_OK": "1"}) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestServiceEnv(t *testing.T) {
	// the service says it is managed, whatever the installing shell says
	got := ServiceEnv(home.Env{"PATH": "/bin", "AGENTD_MANAGED": "0"})
	if !reflect.DeepEqual(got, map[string]string{"PATH": "/bin", "AGENTD_MANAGED": "1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestEnvNamesSortAsLocaleCompareDoes(t *testing.T) {
	// the order JavaScript's localeCompare gave the plists of the installs that exist (checked with bun)
	want := []string{"AGENTD_1", "AGENTD_A", "AGENTD_AGENT_CLAUDE", "AGENTD_AGENTS", "agentd_b", "AGENTD_B", "AGENTD_HOME", "AGENTD_X", "AGENTD1", "DISPLAY", "Path", "PATH", "PATH_X", "PATHX", "XDG_CONFIG_HOME"}
	env := map[string]string{}
	for _, k := range want {
		env[k] = "v"
	}
	if got := sortedNames(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPlist(t *testing.T) {
	t.Run("snapshot: starts at login, KeepAlive only after a failed exit, ThrottleInterval, logs, escaped values", func(t *testing.T) {
		got := Plist(PlistInput{
			Label:    "com.agentd.daemon",
			Launcher: "/Users/ann/.agentd/bin/agentd",
			WorkDir:  "/Users/ann & co",
			LogFile:  "/Users/ann/.agentd/logs/daemon.log",
			Env:      map[string]string{"PATH": "/a&b:/usr/bin", "AGENTD_HOME": "/Users/ann/<x>"},
		})
		want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.agentd.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/ann/.agentd/bin/agentd</string>
    <string>run</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/Users/ann &amp; co</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>AGENTD_HOME</key>
    <string>/Users/ann/&lt;x&gt;</string>
    <key>PATH</key>
    <string>/a&amp;b:/usr/bin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>/Users/ann/.agentd/logs/daemon.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/ann/.agentd/logs/daemon.log</string>
</dict>
</plist>
`
		if got != want {
			t.Fatalf("got:\n%s", got)
		}
	})

	t.Run("no environment, no EnvironmentVariables key", func(t *testing.T) {
		got := Plist(PlistInput{Label: "l", Launcher: "/b", WorkDir: "/h", LogFile: "/l", Env: map[string]string{}})
		if strings.Contains(got, "EnvironmentVariables") {
			t.Fatalf("got:\n%s", got)
		}
	})
}

func TestSystemdUnit(t *testing.T) {
	t.Run("snapshot: Restart=on-failure, RestartSec, logs appended to the daemon log, quoted values", func(t *testing.T) {
		got, err := SystemdUnit(UnitInput{
			Launcher: "/home/ann/.agentd/bin/agentd",
			LogFile:  "/home/ann/.agentd/logs/daemon.log",
			Env:      map[string]string{"PATH": "/home/a n/bin:/usr/bin", "AGENTD_X": `a"b%c$d\e`},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `[Unit]
Description=agentd: runs your agents for the apps you allow
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=%h
ExecStart="/home/ann/.agentd/bin/agentd" run
Restart=on-failure
RestartSec=5
TimeoutStopSec=15
Environment="AGENTD_X=a\"b%%c$$d\\e"
Environment="PATH=/home/a n/bin:/usr/bin"
StandardOutput=append:/home/ann/.agentd/logs/daemon.log
StandardError=append:/home/ann/.agentd/logs/daemon.log

[Install]
WantedBy=default.target
`
		if got != want {
			t.Fatalf("got:\n%s", got)
		}
	})

	t.Run("a unit with its own working directory escapes %", func(t *testing.T) {
		unit, err := SystemdUnit(UnitInput{Launcher: "/l", WorkDir: "/data/50%/work", LogFile: "/log", Env: map[string]string{}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(unit, "WorkingDirectory=/data/50%%/work\n") || strings.Contains(unit, "%h") {
			t.Fatalf("got:\n%s", unit)
		}
	})

	t.Run("a path with a line break cannot go into a unit", func(t *testing.T) {
		ok := UnitInput{Launcher: "/l", WorkDir: "/w", LogFile: "/log", Env: map[string]string{}}
		for name, mutate := range map[string]func(*UnitInput){
			"workdir":  func(i *UnitInput) { i.WorkDir = "/w\nExecStartPre=/tmp/x" },
			"log file": func(i *UnitInput) { i.LogFile = "/log\r\nExecStartPre=/tmp/x" },
			"launcher": func(i *UnitInput) { i.Launcher = "/l\n" },
		} {
			in := ok
			mutate(&in)
			if _, err := SystemdUnit(in); err == nil || !strings.Contains(err.Error(), "line break") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
}

func TestParsers(t *testing.T) {
	print := func(extra string) string {
		return "gui/501/x = {\n\tpath = /tmp/x.plist\n" + extra + "\n}"
	}

	t.Run("launchctl print: the first path, state and pid lines", func(t *testing.T) {
		text := "gui/501/com.agentd.daemon = {\n" +
			"\tactive count = 1\n" +
			"\tpath = /tmp/x/com.agentd.daemon.plist\n" +
			"\ttype = LaunchAgent\n" +
			"\tstate = running\n\n" +
			"\tprogram = /Users/ann/.agentd/bin/agentd\n" +
			"\tpid = 4242\n" +
			"\tlast exit code = (never exited)\n" +
			"\tproperties = keepalive\n}"
		want := Running{Running: true, PID: 4242, Path: "/tmp/x/com.agentd.daemon.plist"}
		if got := ParseLaunchctlPrint(text); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("launchctl print: a job that exited 0 is stopped, one that crashed is waiting for its restart", func(t *testing.T) {
		cases := []struct {
			extra   string
			exit    *int
			pending bool
		}{
			{"\tstate = not running\n\tlast exit code = 0", intp(0), false},
			{"\tstate = not running\n\tlast exit code = 1", intp(1), true},
			{"\tstate = not running\n\tlast terminating signal = Killed: 9", intp(-1), true},
			{"\tstate = not running", nil, false},
		}
		for _, c := range cases {
			got := ParseLaunchctlPrint(print(c.extra))
			if got.Running || !reflect.DeepEqual(got.LastExit, c.exit) || got.Pending != c.pending {
				t.Errorf("%q: got %+v", c.extra, got)
			}
		}
	})

	t.Run("systemctl show", func(t *testing.T) {
		got := ParseSystemctlShow("LoadState=loaded\nActiveState=active\nExecMainStatus=0\nMainPID=77\nFragmentPath=/h/u.service\n")
		if want := (Running{Running: true, PID: 77, Path: "/h/u.service", LastExit: intp(0)}); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		got = ParseSystemctlShow("ActiveState=inactive\nExecMainStatus=0\nMainPID=0\nFragmentPath=\n")
		if want := (Running{LastExit: intp(0)}); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		got = ParseSystemctlShow("ActiveState=activating\nExecMainStatus=1\nMainPID=0\n")
		if want := (Running{LastExit: intp(1), Pending: true}); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("loginctl linger", func(t *testing.T) {
		if !LingerOff("Linger=no\n") || LingerOff("Linger=yes\n") {
			t.Fatal("linger")
		}
	})
}

func TestStateText(t *testing.T) {
	// stopped by the user is told from crashed and restarting
	if got := StateText(nil); got != "not installed" {
		t.Errorf("got %q", got)
	}
	if got := StateText(&Running{Running: true, PID: 9}); got != "running (pid 9)" {
		t.Errorf("got %q", got)
	}
	if got := StateText(&Running{Running: true}); got != "running" {
		t.Errorf("got %q", got)
	}
	stopped := StateText(&Running{LastExit: intp(0)})
	if !strings.Contains(stopped, "stopped (it exited 0") || !strings.Contains(stopped, "service start") {
		t.Errorf("got %q", stopped)
	}
	if got := StateText(&Running{LastExit: intp(1), Pending: true}); got != "restarting after a crash (last exit code 1)" {
		t.Errorf("got %q", got)
	}
	if got := StateText(&Running{}); got != "loaded, not running" {
		t.Errorf("got %q", got)
	}
}
