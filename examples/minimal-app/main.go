// Command minimal-app is the smallest app that uses agentd: a Go server on the hub library and a page on the
// @agentd/client SDK. It has one user, keeps its pairings in memory, and does one thing with the daemon on the user's
// computer: opens an agent and reads its answer to `initialize`.
//
//	minimal-app -addr 127.0.0.1:8090 -static ./static -agentd-url http://127.0.0.1:7100
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	socket "github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/nguyenvanduocit/agentd/hub"
)

// user is the only user this example has; a real app has accounts and passes the signed-in user's id.
const user = "demo"

const askTimeout = 15 * time.Second

type app struct {
	hub       *hub.Hub
	agentdURL string
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("minimal-app: write: %v", err)
	}
}

func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}

// listOr answers with the user's daemons, or the error.
func listOr(w http.ResponseWriter, list hub.List, err error) {
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	reply(w, http.StatusOK, list)
}

// ping opens an agent on the user's most recently connected daemon, sends it `initialize` and returns its first answer line.
func (a *app) ping(ctx context.Context) (string, error) {
	online := a.hub.Online(user)
	if len(online) == 0 {
		return "", hub.ErrNoDaemon
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	conn, err := a.hub.Open(ctx, user, online[0].ID, "claude")
	if err != nil {
		return "", fmt.Errorf("open an agent: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`); err != nil {
		return "", fmt.Errorf("write to the agent: %w", err)
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(conn).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		return s, nil
	case <-ctx.Done():
		return "", errors.New("the agent did not answer")
	}
}

func (a *app) routes(mux *http.ServeMux) {
	me := a.hub.For(user)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]string{"agentdUrl": a.agentdURL})
	})
	mux.HandleFunc("GET /api/daemons", func(w http.ResponseWriter, _ *http.Request) {
		list, err := me.List()
		listOr(w, list, err)
	})
	mux.HandleFunc("POST /api/pairing", func(w http.ResponseWriter, _ *http.Request) {
		list, err := me.StartPairing()
		listOr(w, list, err)
	})
	mux.HandleFunc("DELETE /api/pairing", func(w http.ResponseWriter, _ *http.Request) {
		list, err := me.CancelPairing()
		listOr(w, list, err)
	})
	mux.HandleFunc("DELETE /api/daemons/{id}", func(w http.ResponseWriter, r *http.Request) {
		list, err := me.Revoke(r.PathValue("id"))
		listOr(w, list, err)
	})
	mux.HandleFunc("POST /api/ping", func(w http.ResponseWriter, r *http.Request) {
		line, err := a.ping(r.Context())
		if err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		reply(w, http.StatusOK, map[string]string{"line": line})
	})
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	static := flag.String("static", "static", "directory of the page (index.html, app.js)")
	agentd := flag.String("agentd-url", "http://127.0.0.1:7100", "the agentd release server the page tells the user to install from")
	flag.Parse()

	h := hub.New(hub.Options{
		Tokens: hub.NewMemoryTokens(),
	})
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(hub.MaxMessageBytes)
	io := socket.NewServer(nil, opts)
	defer io.Close(nil)

	mux := http.NewServeMux()
	h.Mount(io, mux)
	mux.Handle("/socket.io/", io.ServeHandler(nil))
	(&app{hub: h, agentdURL: *agentd}).routes(mux)
	mux.Handle("/", http.FileServer(http.Dir(*static)))

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("minimal-app: serving on http://%s (agentd at %s)", *addr, *agentd)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
