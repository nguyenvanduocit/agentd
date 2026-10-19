import { describe, expect, test } from "bun:test"
import {
  lingerOff,
  parseLaunchctlPrint,
  parseSystemctlShow,
  passthroughEnv,
  plist,
  serviceFile,
  serviceEnv,
  serviceName,
  stateText,
  systemdUnit,
} from "../../src/core/service.ts"

describe("names and files", () => {
  test("the user's service has the fixed label and unit name; the seams rename and relocate it", () => {
    expect(serviceName("launchd", {})).toBe("com.agentd.daemon")
    expect(serviceName("systemd", {})).toBe("agentd")
    expect(serviceName("launchd", { AGENTD_SERVICE_LABEL: "com.agentd.daemon.test.ab12" })).toBe("com.agentd.daemon.test.ab12")
    expect(serviceFile("launchd", "com.agentd.daemon", "/Users/ann", {})).toBe("/Users/ann/Library/LaunchAgents/com.agentd.daemon.plist")
    expect(serviceFile("systemd", "agentd", "/home/ann", {})).toBe("/home/ann/.config/systemd/user/agentd.service")
    expect(serviceFile("systemd", "agentd", "/home/ann", { XDG_CONFIG_HOME: "/x" })).toBe("/x/systemd/user/agentd.service")
    expect(serviceFile("launchd", "t", "/Users/ann", { AGENTD_SERVICE_DIR: "/tmp/d" })).toBe("/tmp/d/t.plist")
  })
})

describe("passthroughEnv", () => {
  test("PATH, the XDG directories and AGENTD_* go into the service; the rest of the shell does not", () => {
    expect(
      passthroughEnv({
        PATH: "/opt/homebrew/bin:/usr/bin",
        HOME: "/Users/ann",
        SECRET_TOKEN: "x",
        XDG_CONFIG_HOME: "/c",
        XDG_DATA_HOME: "/d",
        AGENTD_HOME: "/mc",
        AGENTD_AGENT_CLAUDE: "/fake",
        AGENTD_RESTART: "loop",
        AGENTD_MANAGED: "0",
        AGENTD_SERVICE_LABEL: "t",
        AGENTD_SERVICE_DIR: "/d",
        EMPTY: "",
      }),
    ).toEqual({ PATH: "/opt/homebrew/bin:/usr/bin", XDG_CONFIG_HOME: "/c", AGENTD_HOME: "/mc", AGENTD_AGENT_CLAUDE: "/fake" })
  })
})

describe("passthroughEnv and the tray", () => {
  test("the variables that name the user's screen go into the service: the tray finds the display with them", () => {
    expect(passthroughEnv({ DISPLAY: ":0", WAYLAND_DISPLAY: "wayland-0", XAUTHORITY: "/home/ann/.Xauthority", LANG: "C" })).toEqual({
      DISPLAY: ":0",
      WAYLAND_DISPLAY: "wayland-0",
      XAUTHORITY: "/home/ann/.Xauthority",
    })
  })
})

describe("plist", () => {
  test("snapshot: starts at login, KeepAlive only after a failed exit, ThrottleInterval, logs, escaped values", () => {
    expect(
      plist({
        label: "com.agentd.daemon",
        launcher: "/Users/ann/.agentd/bin/agentd",
        workDir: "/Users/ann & co",
        logFile: "/Users/ann/.agentd/logs/daemon.log",
        env: { PATH: "/a&b:/usr/bin", AGENTD_HOME: "/Users/ann/<x>" },
      }),
    ).toBe(`<?xml version="1.0" encoding="UTF-8"?>
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
`)
  })

  test("no environment, no EnvironmentVariables key", () => {
    expect(plist({ label: "l", launcher: "/b", workDir: "/h", logFile: "/l", env: {} })).not.toContain("EnvironmentVariables")
  })
})

describe("systemdUnit", () => {
  test("snapshot: Restart=on-failure, RestartSec, logs appended to the daemon log, quoted values", () => {
    expect(
      systemdUnit({
        launcher: "/home/ann/.agentd/bin/agentd",
        workDir: undefined,
        logFile: "/home/ann/.agentd/logs/daemon.log",
        env: { PATH: "/home/a n/bin:/usr/bin", AGENTD_X: 'a"b%c$d\\e' },
      }),
    ).toBe(`[Unit]
Description=agentd: runs your agents for the apps you allow
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=%h
ExecStart="/home/ann/.agentd/bin/agentd" run
Restart=on-failure
RestartSec=5
TimeoutStopSec=15
Environment="AGENTD_X=a\\"b%%c$$d\\\\e"
Environment="PATH=/home/a n/bin:/usr/bin"
StandardOutput=append:/home/ann/.agentd/logs/daemon.log
StandardError=append:/home/ann/.agentd/logs/daemon.log

[Install]
WantedBy=default.target
`)
  })
})

describe("serviceEnv", () => {
  test("the service says it is managed, whatever the installing shell says", () => {
    expect(serviceEnv({ PATH: "/bin", AGENTD_MANAGED: "0" })).toEqual({ PATH: "/bin", AGENTD_MANAGED: "1" })
  })
})

test("a unit with its own working directory escapes %", () => {
  const unit = systemdUnit({ launcher: "/l", workDir: "/data/50%/work", logFile: "/log", env: {} })
  expect(unit).toContain("WorkingDirectory=/data/50%%/work\n")
  expect(unit).not.toContain("%h")
})

test("a path with a line break cannot go into a unit", () => {
  const ok = { launcher: "/l", workDir: "/w", logFile: "/log", env: {} }
  for (const bad of [{ workDir: "/w\nExecStartPre=/tmp/x" }, { logFile: "/log\r\nExecStartPre=/tmp/x" }, { launcher: "/l\n" }]) {
    expect(() => systemdUnit({ ...ok, ...bad })).toThrow("line break")
  }
})

describe("parsers", () => {
  const print = (extra: string) => `gui/501/x = {
\tpath = /tmp/x.plist
${extra}
}`

  test("launchctl print: the first path, state and pid lines", () => {
    const text = `gui/501/com.agentd.daemon = {
\tactive count = 1
\tpath = /tmp/x/com.agentd.daemon.plist
\ttype = LaunchAgent
\tstate = running

\tprogram = /Users/ann/.agentd/bin/agentd
\tpid = 4242
\tlast exit code = (never exited)
\tproperties = keepalive
}`
    expect(parseLaunchctlPrint(text)).toEqual({ running: true, pid: 4242, path: "/tmp/x/com.agentd.daemon.plist", lastExit: undefined, pending: false })
  })

  test("launchctl print: a job that exited 0 is stopped, one that crashed is waiting for its restart", () => {
    expect(parseLaunchctlPrint(print("\tstate = not running\n\tlast exit code = 0"))).toMatchObject({ running: false, lastExit: 0, pending: false })
    expect(parseLaunchctlPrint(print("\tstate = not running\n\tlast exit code = 1"))).toMatchObject({ running: false, lastExit: 1, pending: true })
    expect(parseLaunchctlPrint(print("\tstate = not running\n\tlast terminating signal = Killed: 9"))).toMatchObject({ lastExit: -1, pending: true })
    expect(parseLaunchctlPrint(print("\tstate = not running"))).toMatchObject({ lastExit: undefined, pending: false })
  })

  test("systemctl show", () => {
    expect(parseSystemctlShow("LoadState=loaded\nActiveState=active\nExecMainStatus=0\nMainPID=77\nFragmentPath=/h/u.service\n")).toEqual({
      running: true,
      pid: 77,
      path: "/h/u.service",
      lastExit: 0,
      pending: false,
    })
    expect(parseSystemctlShow("ActiveState=inactive\nExecMainStatus=0\nMainPID=0\nFragmentPath=\n")).toMatchObject({ running: false, pid: undefined, path: undefined, lastExit: 0, pending: false })
    expect(parseSystemctlShow("ActiveState=activating\nExecMainStatus=1\nMainPID=0\n")).toMatchObject({ running: false, lastExit: 1, pending: true })
  })

  test("loginctl linger", () => {
    expect(lingerOff("Linger=no\n")).toBe(true)
    expect(lingerOff("Linger=yes\n")).toBe(false)
  })
})

describe("stateText", () => {
  const base = { pid: undefined, path: undefined, lastExit: undefined, pending: false }
  test("stopped by the user is told from crashed and restarting", () => {
    expect(stateText(undefined)).toBe("not installed")
    expect(stateText({ ...base, running: true, pid: 9 })).toBe("running (pid 9)")
    expect(stateText({ ...base, running: false, lastExit: 0 })).toContain("stopped (it exited 0")
    expect(stateText({ ...base, running: false, lastExit: 0 })).toContain("service start")
    expect(stateText({ ...base, running: false, lastExit: 1, pending: true })).toBe("restarting after a crash (last exit code 1)")
    expect(stateText({ ...base, running: false })).toBe("loaded, not running")
  })
})
