// Package goserver connects an app's server to the agentd daemons on its users' computers.
//
// A user installs agentd on their computer and pairs it with the app: the app shows a pairing code, the user gives it
// to their daemon, and the daemon dials out to the app over Socket.IO (namespace /agentd) with a token it got by
// pairing. From then on the app can start agents on that computer (Claude Code, Codex, Grok) and speak ACP with them
// over a Conn, as it would over a local pipe. A user pairs any number of daemons; the Server makes each look like a
// place agents can be opened, and tells the user's browser tabs which are there (Watch).
//
// A first app, in full (examples/minimal-app is the same with a page):
//
//	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()}) // your own Tokens store in production
//	mux := http.NewServeMux()
//	defer h.Attach(mux)() // serves /socket.io/, the /agentd namespace and POST /agentd/pair
//
//	list, _ := h.StartPairing(userID)       // show list.Pairing.Code; the user runs: agentd pair --app <your origin> --code <code>
//	conn, err := h.OpenLatest(ctx, userID, "claude") // an ACP stream to the newest online daemon
//
// Errors are values to test with errors.Is and errors.As: ErrUnknownDaemon, OfflineError, IncompatibleError,
// AgentNotReadyError, ErrNoDaemon, ErrNoOnlineDaemon, ErrBadName, ErrNameTaken. Their texts are sentences for the user;
// README.md lists what each means and what to tell them.
//
// Limits: pairing codes, wrong-code counts and daemon connections live in the memory of one process, so one process
// serves a Server; an app with several instances sends /agentd, /agentd/pair and the calls that start a pairing to one
// of them.
package goserver
