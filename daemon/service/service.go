// Package service is `agentd service install|uninstall|start|restart|status|logs`: the launchd agent on macOS, the
// systemd user unit on Linux, and the launcher's own restart loop where neither answers. The text of the definitions,
// the names and files, and the parsers of what the managers print are pure (this file); the commands that run
// launchctl and systemctl and touch the disk take their machine as Deps.
package service

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// ManagerName is what keeps the daemon running.
type ManagerName string

// The managers: a service manager of the OS, or the launcher's restart loop.
const (
	Launchd ManagerName = "launchd"
	Systemd ManagerName = "systemd"
	Loop    ManagerName = "loop"
)

const (
	// LaunchdLabel is the launchd label of the user's service; app/service.go and the app's CLI takeover use it too.
	LaunchdLabel = "com.agentd.daemon"
	// SystemdUnitName is the name of the user's systemd unit.
	SystemdUnitName = "agentd"
	// RestartSeconds is how long the service manager waits between two starts.
	RestartSeconds = 5
)

// ServiceName is the launchd label or the systemd unit name.
//
// TEST SEAMS: $AGENTD_SERVICE_LABEL renames the service and $AGENTD_SERVICE_DIR is where its definition file is
// written (ServiceFile). A test never touches the user's real service.
func ServiceName(manager ManagerName, env home.Env) string {
	if own := env["AGENTD_SERVICE_LABEL"]; own != "" {
		return own
	}
	if manager == Launchd {
		return LaunchdLabel
	}
	return SystemdUnitName
}

// ServiceFile is the definition file of a launchd or systemd service: the plist or the unit.
func ServiceFile(manager ManagerName, name, homeDir string, env home.Env) string {
	if dir := env["AGENTD_SERVICE_DIR"]; dir != "" {
		return path.Join(dir, fileName(manager, name))
	}
	if manager == Launchd {
		return path.Join(homeDir, "Library", "LaunchAgents", fileName(manager, name))
	}
	config := env["XDG_CONFIG_HOME"]
	if config == "" {
		config = path.Join(homeDir, ".config")
	}
	return path.Join(config, "systemd", "user", fileName(manager, name))
}

func fileName(manager ManagerName, name string) string {
	if manager == Launchd {
		return name + ".plist"
	}
	return name + ".service"
}

// copiedIntoService: a service starts with an empty environment, so PATH (where node, npm and the agents are), the
// seams of the tests (AGENTD_*), the XDG directories and the variables that name the user's screen (the tray program
// needs them to find it) are copied into its definition. AGENTD_RESTART, the service seams themselves and
// AGENTD_CONSENT are not: a seam that answers the consent dialog, left in a shell that installs the service, would let
// every page in without asking the user. Nor is what belongs to one run or one secret: the definition is a file other
// users of the machine can read, and a descriptor, a pairing code or a session token means nothing to the next start.
func copiedIntoService(name string) bool {
	if name == "AGENTD_RESTART" || name == "AGENTD_MANAGED" || name == "AGENTD_CONSENT" || strings.HasPrefix(name, "AGENTD_SERVICE_") {
		return false
	}
	switch name {
	case "AGENTD_UI_FD", "AGENTD_PAIR_CODE", "AGENTD_MCP_SOCKET", "AGENTD_MCP_TOKEN":
		return false
	}
	switch name {
	case "PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY":
		return true
	}
	return strings.HasPrefix(name, "AGENTD_")
}

// PassthroughEnv is what the installing shell passes on to the service.
func PassthroughEnv(env home.Env) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if v == "" || strings.ContainsAny(v, "\n\r\x00") {
			continue
		}
		if copiedIntoService(k) {
			out[k] = v
		}
	}
	return out
}

// ServiceEnv is the environment of the service: what the installing shell passes on, and the word that says a
// service restarts the daemon.
func ServiceEnv(env home.Env) map[string]string {
	out := PassthroughEnv(env)
	out["AGENTD_MANAGED"] = "1"
	return out
}

// sortedNames orders environment names as JavaScript's localeCompare does, which is what wrote the definitions of the
// installs that exist: punctuation (the "_" of AGENTD_HOME) before digits before letters, letters without regard to
// case. The bytes of an installed plist stay the same when `service install` runs again.
func sortedNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return compareNames(names[i], names[j]) < 0 })
	return names
}

func collationClass(r rune) (class int, weight rune) {
	switch {
	case unicode.IsLetter(r):
		return 2, unicode.ToLower(r)
	case unicode.IsDigit(r):
		return 1, r
	}
	return 0, r
}

func compareNames(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		ca, wa := collationClass(ra[i])
		cb, wb := collationClass(rb[i])
		if ca != cb {
			return ca - cb
		}
		if wa != wb {
			return int(wa) - int(wb)
		}
	}
	if len(ra) != len(rb) {
		return len(ra) - len(rb)
	}
	for i := range ra { // the same letters: the lowercase one first
		if ra[i] != rb[i] {
			if unicode.IsLower(ra[i]) {
				return -1
			}
			return 1
		}
	}
	return 0
}

var xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// PlistInput is what the LaunchAgent is made from.
type PlistInput struct {
	Label    string
	Launcher string
	// WorkDir is the daemon's working directory: launchd would start it in `/`.
	WorkDir string
	LogFile string
	Env     map[string]string
}

// Plist is the LaunchAgent: it starts at login; launchd starts the launcher again when it exits non-zero, and leaves
// it after exit 0.
func Plist(i PlistInput) string {
	var env strings.Builder
	for _, k := range sortedNames(i.Env) {
		fmt.Fprintf(&env, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape.Replace(k), xmlEscape.Replace(i.Env[k]))
	}
	envBlock := ""
	if env.Len() > 0 {
		envBlock = "  <key>EnvironmentVariables</key>\n  <dict>\n" + env.String() + "  </dict>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + xmlEscape.Replace(i.Label) + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape.Replace(i.Launcher) + `</string>
    <string>run</string>
  </array>
  <key>WorkingDirectory</key>
  <string>` + xmlEscape.Replace(i.WorkDir) + `</string>
` + envBlock + `  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>` + strconv.Itoa(RestartSeconds) + `</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>` + xmlEscape.Replace(i.LogFile) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlEscape.Replace(i.LogFile) + `</string>
</dict>
</plist>
`
}

// UnitInput is what the systemd user unit is made from.
type UnitInput struct {
	Launcher string
	// WorkDir is the daemon's working directory; empty is the user's home (%h).
	WorkDir string
	LogFile string
	Env     map[string]string
}

var unitQuoter = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")

// unitQuote is systemd's quoting inside "...": backslash, double quote, and % and $ which systemd would expand.
func unitQuote(s string) string {
	return `"` + unitQuoter.Replace(s) + `"`
}

// SystemdUnit is the systemd user unit: it starts at login (default.target); systemd starts the launcher again when
// it exits non-zero, and leaves it after exit 0. A unit is read line by line, so a path with a line break (it would
// start a directive of its own) is an error.
func SystemdUnit(i UnitInput) (string, error) {
	for _, p := range []string{i.Launcher, i.WorkDir, i.LogFile} {
		if strings.ContainsAny(p, "\n\r") {
			return "", fmt.Errorf("%s: a path with a line break cannot go into a systemd unit", strconv.Quote(p))
		}
	}
	var env strings.Builder
	for _, k := range sortedNames(i.Env) {
		env.WriteString("Environment=" + unitQuote(k+"="+i.Env[k]) + "\n")
	}
	workDir := "%h"
	if i.WorkDir != "" {
		workDir = strings.ReplaceAll(i.WorkDir, "%", "%%")
	}
	logFile := strings.ReplaceAll(i.LogFile, "%", "%%")
	return `[Unit]
Description=agentd: runs your agents for the apps you allow
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=` + workDir + `
ExecStart=` + unitQuote(i.Launcher) + ` run
Restart=on-failure
RestartSec=` + strconv.Itoa(RestartSeconds) + `
TimeoutStopSec=15
` + env.String() + `StandardOutput=append:` + logFile + `
StandardError=append:` + logFile + `

[Install]
WantedBy=default.target
`, nil
}

// Running is what a manager says about the service.
type Running struct {
	Running bool
	// PID is 0 when the manager does not say.
	PID int
	// Path is the definition file the manager loaded, when it says.
	Path string
	// LastExit is how the last run ended, when the manager says: the exit code, or -1 for a signal.
	LastExit *int
	// Pending: the manager has a restart scheduled (after a crash, within its throttle).
	Pending bool
}

func intp(n int) *int { return &n }

func parseInt(s string) *int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &n
}

func launchctlExit(code, signal string, hasSignal bool) *int {
	if n := parseInt(code); n != nil {
		return n
	}
	if hasSignal {
		return intp(-1)
	}
	return nil
}

// ParseLaunchctlPrint reads `launchctl print gui/<uid>/<label>`: the top-level `path`, `state` and `pid` lines come
// before any nested block.
func ParseLaunchctlPrint(text string) Running {
	first := func(key string) (string, bool) {
		m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + ` = (.+)$`).FindStringSubmatch(text)
		if m == nil {
			return "", false
		}
		return strings.TrimSpace(m[1]), true
	}
	pid, _ := first("pid")
	code, _ := first("last exit code")
	signal, hasSignal := first("last terminating signal")
	state, _ := first("state")
	path, _ := first("path")
	lastExit := launchctlExit(code, signal, hasSignal)
	running := state == "running"
	r := Running{Running: running, Path: path, LastExit: lastExit, Pending: !running && lastExit != nil && *lastExit != 0}
	if n := parseInt(pid); n != nil {
		r.PID = *n
	}
	return r
}

// ParseSystemctlShow reads `systemctl --user show -p ActiveState,ExecMainStatus,MainPID,FragmentPath <unit>`: KEY=VALUE lines.
func ParseSystemctlShow(text string) Running {
	v := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "="); i > 0 {
			v[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	r := Running{Running: v["ActiveState"] == "active", Path: v["FragmentPath"], Pending: v["ActiveState"] == "activating"}
	if pid := parseInt(v["MainPID"]); pid != nil && *pid > 0 {
		r.PID = *pid
	}
	r.LastExit = parseInt(v["ExecMainStatus"])
	return r
}

// StateText is the line of `service status`: what the daemon is doing, told apart by how it last exited
// (daemon/home/exit.go). A nil Running is a service that is not installed.
func StateText(now *Running) string {
	switch {
	case now == nil:
		return "not installed"
	case now.Running && now.PID == 0:
		return "running"
	case now.Running:
		return "running (pid " + strconv.Itoa(now.PID) + ")"
	case now.Pending:
		last := "?"
		if now.LastExit != nil {
			last = strconv.Itoa(*now.LastExit)
		}
		return "restarting after a crash (last exit code " + last + ")"
	case now.LastExit != nil && *now.LastExit == 0:
		return "stopped (it exited 0: a Stop from the web UI, or a revoked token). Start it again with: agentd service start"
	}
	return "loaded, not running"
}

var lingerNo = regexp.MustCompile(`(?m)^Linger=no$`)

// LingerOff reads `loginctl show-user <user> -p Linger`: without linger the service stops when the last session ends.
func LingerOff(text string) bool {
	return lingerNo.MatchString(text)
}
