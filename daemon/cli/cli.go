// Package cli parses the command line of agentd into a Command. It reads nothing but its argument list; the entry
// switches on the Command type and runs it.
package cli

import "errors"

// Command is what the command line asks for: Run, Pair, MCP, PageMCP, Apps, ACP, Version or Help.
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

// ACP prints the address a standard ACP client (an editor, a script) connects to. Agent is the one agent asked for, or
// empty for every agent that is ready. Token prints the token alone; Rotate makes a new one first.
type ACP struct {
	Agent  string
	Token  bool
	Rotate bool
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
func (ACP) command()     {}
func (Version) command() {}
func (Help) command()    {}

// Usage is the help text, printed for help, --help and -h (to stdout, exit 0) and after a parse error (to stderr, exit 2).
const Usage = `agentd: runs your agents for the apps you allow

usage:
  agentd pair --app <url> [--code <code>]   pair this machine with an app; code is read from stdin if omitted
  agentd [run] [--app <url>] [--dir <dir>]  connect and serve agents (default command)
  agentd apps [list]                        the apps and web pages this machine is connected to
  agentd apps remove <origin>               disconnect one of them; a running agentd drops it within seconds
  agentd acp [<agent>] [--rotate] [--token] the address that ACP apps (editors, scripts) connect to, with its token
  agentd help                               this text
  agentd version                            the version

options:
  --app <url>      the app, by its origin (default: every app saved by pair)
  --dir <dir>      working directory of the agents (default: the current directory)
  --config <file>  the config file (default: <config dir>/agentd/daemon.json)
  --rotate         acp: make a new token first; the old one stops working at once
  --token          acp: print the token alone
  -h, --help       this text
  -v, --version    the version

examples:
  agentd pair --app https://app.example.com --code 123456
  agentd apps remove https://app.example.com
  agentd acp claude
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
	if v.version {
		return Version{}, nil
	}
	var name, rest, extra *string
	for i, slot := range []**string{&name, &rest, &extra} {
		if i < len(positionals) {
			*slot = &positionals[i]
		}
	}
	if flag := acpOnlyFlag(v); flag != "" && (name == nil || *name != "acp") {
		return nil, errors.New(flag + ` only works with "agentd acp"`)
	}
	if extra != nil && *name != "apps" {
		return nil, errors.New(`unexpected argument "` + *extra + `"`)
	}
	if len(positionals) > 3 {
		return nil, errors.New(`unexpected argument "` + positionals[3] + `"`)
	}
	return build(name, rest, extra, v)
}

// acpOnlyFlag is the option, if one was given, that only `agentd acp` has a use for: with any other command it would be
// accepted and ignored, and `agentd --token` would start a daemon.
func acpOnlyFlag(v flagValues) string {
	switch {
	case v.token:
		return "--token"
	case v.rotate:
		return "--rotate"
	}
	return ""
}

func build(name, rest, extra *string, v flagValues) (Command, error) {
	if name != nil && *name == "apps" {
		return apps(rest, extra, v)
	}
	if name != nil && *name == "acp" {
		agent := ""
		if rest != nil {
			agent = *rest
		}
		return ACP{Agent: agent, Token: v.token, Rotate: v.rotate}, nil
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
	case "help":
		return Help{}, nil
	case "version":
		return Version{}, nil
	case "pair":
		return pair(v)
	case "mcp":
		return mcp(v)
	}
	return nil, errors.New(`unknown command "` + *name + `"`)
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
