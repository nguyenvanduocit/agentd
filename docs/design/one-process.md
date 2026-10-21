# agentd as one process

agentd.app is the app and the engine in one process: one executable, `agentd.app/Contents/MacOS/agentd`.

## Parts

1. **The executable** is the app when it is started with no arguments, and agentd's command line when it is started with
   a command (`mcp`, `acp`, `apps`, `pair`, `run`, `version`). `daemon/entry` is the command line; `cmd/agentd` and the app
   both call it, and the agents' MCP wrappers and the user's terminal call the app's executable.
2. **The engine** (`daemon/run`) is a library the app calls: `run.Daemon(ctx, cmd, deps)` in a goroutine. Its exit codes
   keep their meaning (0 stopped, 75 start again, anything else a crash), so the app's supervision (`app/supervise.go`) is
   unchanged: `AfterExit`, the quick-stop wait and the crash count.
3. **The link.** The engine shows its state and asks the user through Go values: `run.Deps.UI` opens a `ui.Link` (the app
   implements `Update`) that is also a `ui.Asker` (`Ask` opens the consent window and returns the decision). The app
   calls back with `ui.Action` for the choices that are the engine's: disconnect and restart. With no `UI`, nothing shows
   the state and no question is asked, so nothing is granted.
4. **One definition of the state.** The app draws `daemon/ui.State`, `ui.AppRow` and `ui.AcpState`; `app/` is its own Go
   module that requires the root module (`replace ../`).
5. **The environment** the engine reads is a `home.Host` the app builds: the process environment with the PATH of the
   login shell (the Node.js the app carries first), `AGENTD_HOME`, and `AGENTD_MANAGED=1`.
6. **The version** of the engine is the version in `app/mygo.json` (`app/version.go`).
7. **The build** is one `mygo build`; `scripts/build-desktop.sh` adds Node.js to the resources.

## The headless engine

`cmd/agentd` is the same engine with no window. The daemon tests, the Linux checks, the end-to-end run and
`sdk/goserver/goservertest` use it.

## Costs

- A panic in an engine goroutine ends the app: Go cannot recover another goroutine's panic. The engine's goroutines end
  the process after they stop the process groups they track (`proc.Logger.CrashOnPanic`); the next start ends what
  `sessions.json` records.
- A SIGKILL of the app leaves running agents until the next start.

## Verification

`go vet` and `go test` of the root module and of `app/`; the app run unbundled against a temporary `AGENTD_HOME` (the
gateway answers `/v1/status`, `agentd acp` prints the address, SIGTERM ends the app); the end-to-end scenario against
`cmd/agentd`.
