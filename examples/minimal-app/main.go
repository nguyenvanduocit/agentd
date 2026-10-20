// Command minimal-app is the smallest app that uses agentd: a Go server on sdk/goserver and a page on the
// @agentd/client SDK. It has one user, keeps its pairings in memory, and does one thing with the computer the user
// pairs: opens an agent and shows its answer to `initialize`. README.md says how to run it against a daemon of your own.
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

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// user is the only user this example has. A real app has accounts: pass the signed-in user's id wherever this is used.
const user = "demo"

const askTimeout = 15 * time.Second

type app struct {
	server *goserver.Server
	// agentdURL is where the page tells the user to install agentd from. It is for development and the end-to-end
	// test, which serve their own releases; a real app drops it (and /api/config), and the page SDK points at GitHub.
	agentdURL string
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("minimal-app: write: %v", err)
	}
}

// statusOf is the HTTP status for what goserver returned. Each of its errors is a value to test with errors.Is or
// errors.As (the godoc of the method says which it can return), and most of their texts are sentences for the user,
// so the page shows err.Error() as it is. fallback is the status for an error that is not one of them.
func statusOf(err error, fallback int) int {
	var (
		offline  goserver.OfflineError
		incompat goserver.IncompatibleError
		notReady *goserver.AgentNotReadyError
	)
	switch {
	case errors.Is(err, goserver.ErrUnknownDaemon), errors.Is(err, goserver.ErrNoDaemon):
		return http.StatusNotFound
	case errors.As(err, &offline), errors.As(err, &incompat), errors.Is(err, goserver.ErrNoOnlineDaemon):
		return http.StatusConflict
	case errors.Is(err, goserver.ErrBadName), errors.Is(err, goserver.ErrNameTaken), errors.As(err, &notReady):
		return http.StatusUnprocessableEntity
	}
	return fallback
}

// listOr answers with the user's daemons, or the error of the call that made the list.
func listOr(w http.ResponseWriter, list goserver.List, err error) {
	if err != nil {
		reply(w, statusOf(err, http.StatusInternalServerError), map[string]string{"error": err.Error()})
		return
	}
	reply(w, http.StatusOK, list)
}

// ping opens an agent on the user's newest online computer, sends it `initialize` and returns its first answer line.
func (a *app) ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	conn, err := a.server.OpenLatest(ctx, user, "claude")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	// conn is a raw ACP stream; a real app runs an ACP client over it (github.com/coder/acp-go-sdk).
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
	me := a.server.For(user)
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
	mux.HandleFunc("DELETE /api/daemons/{id}", func(w http.ResponseWriter, r *http.Request) { // curl -X DELETE .../api/daemons/<id>
		list, err := me.Revoke(r.PathValue("id"))
		listOr(w, list, err)
	})
	mux.HandleFunc("PUT /api/daemons/{id}/name", func(w http.ResponseWriter, r *http.Request) { // curl -X PUT -d '{"name":"mac"}' .../name
		var name struct{ Name string }
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&name)
		list, err := me.Rename(r.PathValue("id"), name.Name)
		listOr(w, list, err)
	})
	mux.HandleFunc("POST /api/ping", func(w http.ResponseWriter, r *http.Request) {
		line, err := a.ping(r.Context())
		if err != nil {
			reply(w, statusOf(err, http.StatusBadGateway), map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, map[string]string{"line": line})
	})
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	static := flag.String("static", "static", "directory of the page (index.html, app.js)")
	agentd := flag.String("agentd-url", "http://127.0.0.1:7100", "development and tests only: the releases the page tells the user to install from")
	flag.Parse()

	// MemoryTokens forgets every pairing when the process ends. A real app keeps them in its database by implementing
	// goserver.Tokens (ByHash, ByID, List, Add, Rename, Delete, Seen); the godoc says what each owes the user condition.
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
	mux := http.NewServeMux()
	defer h.Attach(mux)() // /socket.io/, the /agentd namespace and POST /agentd/pair, with the message size daemons need
	(&app{server: h, agentdURL: *agentd}).routes(mux)
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
