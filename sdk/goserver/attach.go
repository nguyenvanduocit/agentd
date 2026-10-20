package goserver

import (
	"net/http"

	socket "github.com/zishang520/socket.io/servers/socket/v3"
)

// SocketOptions are the options of the Socket.IO server that daemons connect to. They hold one thing that matters: a
// message of up to MaxMessageBytes, because the default of 1 MB ends a daemon's session on a large tool result. Use
// them when the app runs a Socket.IO server of its own and calls Mount; Attach uses them for the one it makes.
//
// Since 0.7.0.
func SocketOptions() *socket.ServerOptions {
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(MaxMessageBytes)
	return opts
}

// Attach wires the Server onto mux in one call: the /agentd Socket.IO namespace daemons connect to, the /socket.io/
// handler that serves it (with SocketOptions), and POST /agentd/pair, where a daemon trades a pairing code for its
// token. The returned function closes the Socket.IO server and every daemon connection on it; call it when the web
// server shuts down.
//
// Attach registers "/socket.io/" on mux, so it is for an app that has no Socket.IO server of its own. An app that has
// one calls Mount on it and gives it SocketOptions: Mount alone does not serve /socket.io/, and a daemon that cannot
// reach it retries for ever while the app shows the daemon as offline.
//
// Since 0.7.0.
func (h *Server) Attach(mux *http.ServeMux) (shutdown func()) {
	io := socket.NewServer(nil, SocketOptions())
	h.Mount(io, mux)
	mux.Handle("/socket.io/", io.ServeHandler(nil))
	return func() { io.Close(nil) }
}
