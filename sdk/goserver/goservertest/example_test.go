package goservertest_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
	"github.com/nguyenvanduocit/agentd/sdk/goserver/goservertest"
)

// A test of an app that runs agents on a user's computer: the app's own server, a real daemon paired with it, and an
// agent opened through the SDK. The body is a test function; it is shown here, and compiled with the package, but not
// run (it builds a daemon the first time).
func ExampleStartDaemon() {
	_ = func(t *testing.T) {
		// The app, as in production: a goserver.Server on a web server that also serves Socket.IO.
		h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
		opts := socket.DefaultServerOptions()
		opts.SetMaxHttpBufferSize(goserver.MaxMessageBytes)
		io := socket.NewServer(nil, opts)
		t.Cleanup(func() { io.Close(nil) })
		mux := http.NewServeMux()
		h.Mount(io, mux)
		mux.Handle("/socket.io/", io.ServeHandler(nil))
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		// A real daemon, paired for the user "u1" and online when this returns. It stops with the test.
		d := goservertest.StartDaemon(t, goservertest.App{URL: srv.URL, Server: h, User: "u1"})

		// The code under test opens an agent on it. The agent is goservertest.FakeAgent: it answers initialize.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := h.Open(ctx, "u1", d.ID(), "claude")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
		line, _ := bufio.NewReader(conn).ReadString('\n')
		t.Log(line) // {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}

		// A test of the app with its daemon gone: Stop returns when the app lists the daemon as offline.
		d.Stop()
		if _, err := h.Open(ctx, "u1", d.ID(), "claude"); err == nil {
			t.Fatal("an offline daemon opened an agent")
		}
	}
}

// An agent of your own in place of the fake one: any program that reads and writes one JSON-RPC message per line.
func ExampleWithAgentCommand() {
	_ = func(t *testing.T, app goservertest.App) {
		d := goservertest.StartDaemon(t, app, goservertest.WithAgentCommand("claude", "/path/to/my-agent --flag"))
		_ = d
	}
}
