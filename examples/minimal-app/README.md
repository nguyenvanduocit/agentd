# minimal-app

The smallest app on agentd: a Go server on [`sdk/goserver`](../../sdk/goserver/README.md) (`main.go`, about 170 lines) and a page on [`@agentd/client`](../../sdk/web/README.md) (`page/`). It has one user, keeps its pairings in memory, and does one thing with a computer the user pairs: opens an agent and shows its answer to `initialize`. It is also the fixture of the end-to-end test (`scripts/e2e.sh`).

Copy `main.go` and change what its comments mark: the user (`demo`), `MemoryTokens` (your database), and the `-agentd-url` flag with `/api/config` (development only; the page SDK points at GitHub by default).

## What it does

| Route | goserver call | Answers |
|---|---|---|
| `GET /api/daemons` | `List` | the user's computers |
| `POST /api/pairing`, `DELETE /api/pairing` | `StartPairing`, `CancelPairing` | the list, with the code |
| `DELETE /api/daemons/{id}` | `Revoke` | the list |
| `PUT /api/daemons/{id}/name` | `Rename` | the list |
| `POST /api/ping` | `OpenLatest`, then one `initialize` line over the `Conn` | `{"line": ...}` |

An error becomes a status that says what to do (`statusOf`): 404 unknown computer or none paired, 409 offline, incompatible or none online, 422 a bad name or an agent that is not ready, 502 anything else; the body is `{"error": "<a sentence for the user>"}`.

## Run it against a daemon of your own

```sh
scripts/build-example.sh /tmp/minimal              # the server and its page, in /tmp/minimal
/tmp/minimal/minimal-app -addr 127.0.0.1:8090 -static /tmp/minimal/static
```

Then pair a daemon that touches nothing of yours. Build `agentd` and the fake agent, give the daemon a home of its own and swap the Claude agent for the fake one, as the "Run it locally" section of the [goserver README](../../sdk/goserver/README.md#run-it-locally) shows, and:

```sh
CODE=$(curl -s -X POST http://127.0.0.1:8090/api/pairing | sed -E 's/.*"code":"([^"]+)".*/\1/')
agentd pair --app http://127.0.0.1:8090 --code "$CODE"
agentd run &
curl -s -X POST http://127.0.0.1:8090/api/ping
# {"line":"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1}}\n"}
```

The page at http://127.0.0.1:8090 does the same from a browser.

To run a second daemon beside your own, pin the ports: start the daemon with `AGENTD_LOCAL_PORT=47700` (its gateway then walks up from 47700 through ten ports) and the app with `-agentd-ports 47700-47709`, so the page looks only there and never pairs with the daemon on 47615. The default (no flag) is the SDK's own 47615-47624.
