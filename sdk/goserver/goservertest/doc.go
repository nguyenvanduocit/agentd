// Package goservertest runs a real agentd against your app's server in your tests.
//
// An app that uses sdk/goserver opens agents on a daemon on the user's computer. To test that code you want a daemon
// that is paired with your app and online, an agent that answers, and a way to make the daemon go away. StartDaemon
// gives you that in one call:
//
//	d := goservertest.StartDaemon(t, goservertest.App{URL: srv.URL, Server: h, User: "u1"})
//	conn, err := h.Open(ctx, "u1", d.ID(), "claude") // your code, against a daemon that is really there
//	d.Stop()                                          // the daemon is gone, and the app lists it as offline
//
// (ExampleStartDaemon is the whole test.) It is the daemon your users run, not a fake: the same pairing, the same
// Socket.IO link, the same agent process handling. Only the agent is replaced, by FakeAgent, which answers
// `initialize`; WithAgentCommand puts a program of your own in its place.
//
// # What it does
//
//   - Builds agentd and the fake agent from the github.com/nguyenvanduocit/agentd module your module depends on, so the
//     daemon is the version of the SDK you import and speaks its protocol. It builds inside that module's own
//     directory (the module cache copy, or the directory a replace points at) with that module's go.mod and go.sum,
//     without cgo and without a go.work.
//   - Gives every daemon a HOME, config, state and working directory of its own under t.TempDir() and a port of its
//     own (AGENTD_LOCAL_PORT=0), turns the adapter install off, and removes every AGENTD_*
//     variable of your environment from the daemon's. It never reads or writes your ~/.config/agentd or ~/.agentd,
//     and never touches the agentd you may have running.
//   - Pairs it with your app (it asks Server.For(User).StartPairing() for the code, or uses WithPairingCode) and
//     returns when the app lists the daemon as online.
//   - Stops the daemon and its process group when the test ends. Stop and Restart let a test go through the offline
//     paths of your app.
//   - When the daemon does not come online, fails the test with what was tried and the end of the daemon's own log, and a
//     hint when the log shows the usual mistake (an app that does not serve /socket.io/).
//
// # What it costs
//
// The first StartDaemon in a test binary compiles the daemon: about 2 s when your machine's Go build cache already holds
// its dependencies, and about 16 s with an empty cache (measured on an Apple M-series laptop; a machine that must also
// download the daemon's dependencies takes what the download takes). After that a daemon is online in about 0.2 s. The
// programs are kept in os.TempDir()/goservertest-agentd/<key> (about 16 MB), one build at a time: a build of
// another agentd version or of a changed source tree replaces it, and the operating system clears the temporary
// directory. The Go toolchain must be installed where the tests run. The daemon does not run on Windows: there
// StartDaemon skips the test (use WSL).
//
// Set GOSERVERTEST_BIN to a directory that holds a built `agentd` and `fake-agent` to build nothing: for a CI that
// builds them once, for an app that vendors its dependencies, or for a machine without Go.
//
// The directory the tests run in must be inside a module that requires github.com/nguyenvanduocit/agentd, which it does
// when it imports sdk/goserver.
//
// # What it does not do
//
// It does not test a real model: the agent is a stand-in. It does not test the page side of agentd (the local gateway
// that a web page talks to); sdk/goclient has the client for that. It is not a fake of the SDK: a test that needs a
// daemon in a state a real one never reaches (a protocol you invented, a half-open socket) still writes its own.
package goservertest
