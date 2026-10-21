package registry

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

const (
	pairTimeout   = 15 * time.Second
	detailRunes   = 200
	pairCodeEnv   = "AGENTD_PAIR_CODE"
	codePrompt    = "Pairing code: "
	noCodeMessage = "no pairing code provided (pass --code <code> or pipe it to stdin)"
)

// PairError is a pairing that failed for a reason the user can act on; its text says what to do.
type PairError struct{ Message string }

func (e *PairError) Error() string { return e.Message }

func pairErrorf(format string, args ...any) *PairError {
	return &PairError{Message: fmt.Sprintf(format, args...)}
}

// Doer is the part of *http.Client that pairing uses.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Machine is what the server learns about the computer that pairs. OS and Arch use the names the server has always
// received from the daemon ("darwin", "linux", "win32"; "arm64", "x64").
type Machine struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// CurrentMachine is this computer. A hostname that cannot be read is empty.
func CurrentMachine() Machine {
	name, _ := os.Hostname()
	return MachineOf(name, runtime.GOOS, runtime.GOARCH)
}

// MachineOf names a computer from its hostname and Go's GOOS and GOARCH.
func MachineOf(hostname, goos, goarch string) Machine {
	osName, arch := goos, goarch
	if goos == "windows" {
		osName = "win32"
	}
	switch goarch {
	case "amd64":
		arch = "x64"
	case "386":
		arch = "ia32"
	}
	return Machine{Hostname: hostname, OS: osName, Arch: arch}
}

// refused are the HTTP statuses of a code the server does not accept.
func refused(status int) bool {
	switch status {
	case 400, 401, 403, 404, 410:
		return true
	}
	return false
}

// detail is ": <first 200 characters of the body>", or nothing for an empty body.
func detail(res *http.Response) string {
	body, _ := io.ReadAll(res.Body) // a body that cannot be read is no detail
	text := []rune(strings.TrimSpace(string(body)))
	if len(text) == 0 {
		return ""
	}
	return ": " + string(text[:min(len(text), detailRunes)])
}

// reason is the useful part of a failed request (connection refused, no such host, a bad port) without the URL.
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func post(ctx context.Context, client Doer, m Machine, origin, code string) (*http.Response, error) {
	body, err := json.Marshal(struct {
		Code string `json:"code"`
		Machine
	}{code, m})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, home.PairURL(origin), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, pairErrorf("cannot reach %s (%s). Check the address and your network, then run the command again.", origin, reason(err))
	}
	return res, nil
}

// Pair trades the single-use code for a token and saves the app to the registry in file, with the access the user
// gave it (its name, folders and end; see Access). It returns the saved app. Nothing is saved unless the server answers with a token.
func Pair(ctx context.Context, client Doer, m Machine, server, code, file string, a Access) (App, error) {
	origin, err := home.ServerOrigin(server)
	if err != nil {
		return App{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pairTimeout)
	defer cancel()
	res, err := post(ctx, client, m, origin, code)
	if err != nil {
		return App{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		if refused(res.StatusCode) {
			return App{}, pairErrorf("%s refused the pairing code (HTTP %d%s). "+
				"A code works once and expires after 10 minutes: copy a fresh command from the app's web UI.", origin, res.StatusCode, detail(res))
		}
		return App{}, pairErrorf("%s failed to pair (HTTP %d%s). Try again in a minute.", origin, res.StatusCode, detail(res))
	}
	var reply map[string]any
	body, _ := io.ReadAll(res.Body)
	if json.Unmarshal(body, &reply) != nil {
		reply = nil
	}
	token, _ := reply["token"].(string)
	if token == "" {
		return App{}, pairErrorf("%s answered without a token: is this an app that serves agentd?", origin)
	}
	app := App{Server: origin, Token: token, Name: a.Name, Folders: a.Folders, ExpiresAt: a.ExpiresAt}
	if _, err := Update(file, func(r Registry) Registry { return r.AddApp(app) }); err != nil {
		return App{}, err
	}
	return app, nil
}

// PairDeps is what RunPair touches outside the registry.
type PairDeps struct {
	Client  Doer
	Machine Machine
	Host    home.Host // the environment AGENTD_PAIR_CODE is read from, and the default registry file
	Log     *proc.Logger
	Stdin   io.Reader
	Prompt  io.Writer // where the code prompt is written (stderr)
}

// readCode reads one non-empty line, used when --code is not on the command line.
func readCode(in io.Reader, prompt io.Writer) (string, error) {
	_, _ = io.WriteString(prompt, codePrompt)
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		if code := strings.TrimSpace(sc.Text()); code != "" {
			return code, nil
		}
	}
	return "", &PairError{Message: noCodeMessage}
}

// pairingCode is --code, else $AGENTD_PAIR_CODE, else a line of stdin.
func pairingCode(cmd cli.Pair, d PairDeps) (string, error) {
	if cmd.Code != nil {
		return *cmd.Code, nil
	}
	if env, ok := d.Host.Env[pairCodeEnv]; ok {
		return env, nil
	}
	return readCode(d.Stdin, d.Prompt)
}

// RunPair is `pair --app <url> [--code <code>]`: the code is --code, else $AGENTD_PAIR_CODE, else a line of stdin.
// It returns the exit code, meaningful when the error is nil.
func RunPair(ctx context.Context, cmd cli.Pair, d PairDeps) (int, error) {
	code, err := pairingCode(cmd, d)
	if err != nil {
		return 0, err
	}
	file := File(cmd.Config, d.Host)
	app, err := Pair(ctx, d.Client, d.Machine, cmd.App, code, file, Access{})
	if err != nil {
		return 0, err
	}
	d.Log.Say(fmt.Sprintf("Paired with %s.", app.Server))
	return 0, nil
}
