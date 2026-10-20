package goserver_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// An app's server in four lines: a Server over a token store, Attach on the web server, and a pairing code for the
// user to give their daemon. These examples are compiled with the package and are not run (they need a daemon).
func ExampleNew() {
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()}) // your own Tokens store in production

	list, err := h.StartPairing("user-1")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("On your computer run: agentd pair --app https://app.example.com --code", list.Pairing.Code)
}

func ExampleServer_Attach() {
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})

	mux := http.NewServeMux()
	shutdown := h.Attach(mux) // serves /socket.io/, the /agentd namespace and POST /agentd/pair
	defer shutdown()

	log.Fatal(http.ListenAndServe("127.0.0.1:8090", mux))
}

// Open starts an agent on a user's daemon and returns its ACP stream. Every error is a value to test with errors.Is or
// errors.As, and most of their texts are sentences you can show the user.
func ExampleServer_Open() {
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := h.Open(ctx, "user-1", "daemon-id", "claude")
	var (
		offline    goserver.OfflineError
		incompat   goserver.IncompatibleError
		notReady   *goserver.AgentNotReadyError
		statusCode int
		message    string
	)
	switch {
	case err == nil:
	case errors.Is(err, goserver.ErrUnknownDaemon):
		statusCode, message = http.StatusNotFound, "That computer is no longer connected to your account."
	case errors.As(err, &offline):
		statusCode, message = http.StatusConflict, err.Error() // "Your daemon "mac" is offline: start it, or pick another daemon"
	case errors.As(err, &incompat):
		statusCode, message = http.StatusConflict, err.Error() // names the side to update
	case errors.As(err, &notReady):
		statusCode, message = http.StatusUnprocessableEntity, err.Error() // notReady.Offered, notReady.Reason say more
	default:
		statusCode, message = http.StatusBadGateway, err.Error() // the daemon refused or did not answer
	}
	if err != nil {
		log.Printf("%d: %s", statusCode, message)
		return
	}
	defer conn.Close()

	// conn is the agent's stdin and stdout: speak ACP over it. One line of JSON-RPC each way is enough to try it.
	if _, err := fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`); err != nil {
		log.Fatal(err)
	}
	answer, _ := bufio.NewReader(conn).ReadString('\n')
	fmt.Println(answer)
}

// A user with several computers usually picks one; an app that does not ask opens on the one that came online last.
func ExampleServer_OpenLatest() {
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
	conn, err := h.OpenLatest(context.Background(), "user-1", "claude")
	switch {
	case errors.Is(err, goserver.ErrNoDaemon):
		fmt.Println("Pair a computer first.")
	case errors.Is(err, goserver.ErrNoOnlineDaemon):
		fmt.Println("Start agentd on one of your computers.")
	case err != nil:
		fmt.Println(err)
	default:
		defer conn.Close()
	}
}

// Watch follows the user's daemons for a page that shows them live; cancel closes the channel.
func ExampleServer_Watch() {
	h := goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})
	lists, cancel := h.Watch("user-1")
	defer cancel()
	for list := range lists {
		for _, d := range list.Daemons {
			fmt.Println(d.Name, d.State) // goserver.StateOnline or goserver.StateOffline; d.Incompatible says why not
		}
	}
}
