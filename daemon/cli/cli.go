// Package cli parses the command line of agentd into a Command. It reads nothing but its argument list; the entry
// switches on the Command type and runs it.
package cli

import (
	"errors"
	"slices"
	"strings"
)

// Command is what the command line asks for: Run, Pair, MCP, PageMCP, Apps, Service, Version or Help.
// An optional string flag is a *string: nil when the flag was not given, so an empty `--dir=` stays distinguishable.
type Command interface{ command() }

// Run connects and serves agents: the default command.
type Run struct {
	App, Dir, Config *string
}

// Pair trades a pairing code for a token. Code is nil when the code comes from the environment or stdin.
type Pair struct {
	App    string
	Code   *string
	Config *string
}

// MCP bridges to an app's server for one session.
type MCP struct {
	Session string
	App     *string
	Config  *string
}

// PageMCP bridges to a page's MCP server; its socket and token come from the environment.
type PageMCP struct{}

// AppsAction is what `agentd apps` does.
type AppsAction string

// The actions of Apps.
const (
	AppsList   AppsAction = "list"
	AppsRemove AppsAction = "remove"
)

// Apps lists the connected apps and pages, or removes one. Origin is set for AppsRemove and empty for AppsList.
type Apps struct {
	Action AppsAction
	Origin string
	Config *string
}

// ServiceAction is what `agentd service` does.
type ServiceAction string

// The actions of Service.
const (
	ServiceInstall   ServiceAction = "install"
	ServiceUninstall ServiceAction = "uninstall"
	ServiceStart     ServiceAction = "start"
	ServiceRestart   ServiceAction = "restart"
	ServiceStatus    ServiceAction = "status"
	ServiceLogs      ServiceAction = "logs"
)

var serviceActions = []ServiceAction{ServiceInstall, ServiceUninstall, ServiceStart, ServiceRestart, ServiceStatus, ServiceLogs}

// DefaultLogLines is how many log lines `service logs` shows without -n.
const DefaultLogLines = 100

// Service manages the login service. Lines and Follow matter to ServiceLogs; Dir to ServiceInstall.
type Service struct {
	Action ServiceAction
	Lines  int
	Follow bool
	Dir    *string
}

// Version prints the version.
type Version struct{}

// Help prints Usage.
type Help struct{}

func (Run) command()     {}
func (Pair) command()    {}
func (MCP) command()     {}
func (PageMCP) command() {}
func (Apps) command()    {}
func (Service) command() {}
func (Version) command() {}
func (Help) command()    {}

// Usage is the help text, printed for --help and after a parse error.
const Usage = `agentd: runs your agents for the apps you allow

usage:
  agentd pair --app <url> [--code <code>]     pair this machine with an app; code is read from stdin if omitted
  agentd [run] [--app <url>] [--dir <dir>]    connect and serve agents (default command)
  agentd apps [list]                         the apps and web pages this machine is connected to
  agentd apps remove <origin>                disconnect one of them (service restart applies it)
  agentd service install [--dir <dir>]       start at login and restart when it dies (launchd, systemd)
  agentd service start|restart               start it again after a Stop from the web UI; restart it
  agentd service uninstall|status
  agentd service logs [-n <lines>] [-f]      the daemon's log
  agentd version

options:
  --app <url>      the app, by its origin (default: every app saved by pair)
  --dir <dir>      working directory of the agents (default: the current directory; service install: your home)
  --config <file>  the config file (default: <config dir>/agentd/daemon.json)
  -n, --lines <n>  service logs: how many lines (default 100)
  -f, --follow     service logs: keep printing
`

// Parse reads the arguments after the program name. A mistake is an error whose text says what is wrong; the entry
// prints it with Usage and exits 2.
func Parse(argv []string) (Command, error) {
	v, positionals, err := parseFlags(argv)
	if err != nil {
		return nil, err
	}
	if v.help {
		return Help{}, nil
	}
	var name, rest, extra *string
	for i, slot := range []**string{&name, &rest, &extra} {
		if i < len(positionals) {
			*slot = &positionals[i]
		}
	}
	if extra != nil && *name != "apps" {
		return nil, errors.New(`unexpected argument "` + *extra + `"`)
	}
	if len(positionals) > 3 {
		return nil, errors.New(`unexpected argument "` + positionals[3] + `"`)
	}
	return build(name, rest, extra, v)
}

func build(name, rest, extra *string, v flagValues) (Command, error) {
	if name != nil && *name == "service" {
		return service(rest, v)
	}
	if name != nil && *name == "apps" {
		return apps(rest, extra, v)
	}
	if rest != nil {
		return nil, errors.New(`unexpected argument "` + *rest + `"`)
	}
	if name == nil {
		return Run{App: v.app, Dir: v.dir, Config: v.config}, nil
	}
	switch *name {
	case "run":
		return Run{App: v.app, Dir: v.dir, Config: v.config}, nil
	case "version":
		return Version{}, nil
	case "pair":
		return pair(v)
	case "mcp":
		return mcp(v)
	}
	return nil, errors.New(`unknown command "` + *name + `"`)
}

func service(action *string, v flagValues) (Command, error) {
	names := make([]string, len(serviceActions))
	for i, a := range serviceActions {
		names[i] = string(a)
	}
	if action == nil || !slices.Contains(names, *action) {
		return nil, errors.New("service needs one of: " + strings.Join(names, ", "))
	}
	lines := DefaultLogLines
	if v.lines != nil {
		n, ok := positiveInt(*v.lines)
		if !ok {
			return nil, errors.New(`--lines "` + *v.lines + `" is not a positive number`)
		}
		lines = n
	}
	return Service{Action: ServiceAction(*action), Lines: lines, Follow: v.follow, Dir: v.dir}, nil
}

func pair(v flagValues) (Command, error) {
	if v.app == nil || *v.app == "" {
		return nil, errors.New("pair needs --app <url>")
	}
	return Pair{App: *v.app, Code: v.code, Config: v.config}, nil
}

// mcp: `--session <id>` bridges to an app's server; `--page` to a page's.
func mcp(v flagValues) (Command, error) {
	if v.page && v.session != nil {
		return nil, errors.New("mcp takes --session <id> or --page, not both")
	}
	if v.page {
		return PageMCP{}, nil
	}
	if v.session == nil || *v.session == "" {
		return nil, errors.New("mcp needs --session <id> (an app's session) or --page (a page's)")
	}
	return MCP{Session: *v.session, App: v.app, Config: v.config}, nil
}

func apps(action, origin *string, v flagValues) (Command, error) {
	if action == nil || *action == "list" {
		return Apps{Action: AppsList, Config: v.config}, nil
	}
	if *action != "remove" {
		return nil, errors.New("apps needs one of: list, remove")
	}
	if origin == nil {
		return nil, errors.New("apps remove needs <origin>, for example https://app.example.com")
	}
	return Apps{Action: AppsRemove, Origin: *origin, Config: v.config}, nil
}
