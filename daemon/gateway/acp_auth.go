package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/acpauth"
)

// acpNeedsToken is what a program without the token is told. It names the way to get one and nothing about the
// endpoint: which agents there are, which are ready and what the path should be are not for a caller who is not in.
const acpNeedsToken = "this computer's agentd needs its local token: run `agentd acp` to get the address"

// acpRefusedLine is the line the log gets for refused programs.
const acpRefusedLine = "acp connection refused: a local program without the token"

// acpRefusedEvery is how often that line may be written: a program that retries must not fill the log.
const acpRefusedEvery = 10 * time.Second

// acpPresented is the token a request offers, and whether it offers one in a way that can be judged. The
// Authorization header is the credential when there is one: `Bearer <token>`, the scheme in any case; a value that is
// not a Bearer one, or two headers, is a refusal and the query is not looked at, so a wrong header is never rescued by
// a right query. Without the header the `token` query parameter is the credential, once. The value is taken as sent:
// a space before or after it makes it another value.
func acpPresented(h http.Header, query url.Values) (token string, offered bool) {
	switch auth := h.Values("Authorization"); len(auth) {
	case 0:
		values := query["token"]
		if len(values) != 1 {
			return "", false
		}
		return values[0], true
	case 1:
		scheme, value, found := strings.Cut(auth[0], " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return "", false
		}
		return value, true
	}
	return "", false
}

// acpTokenOK is whether a request from a program (one with no Origin header) presents the token. With no source of
// the token, or one that gives none, nobody does.
func (s *server) acpTokenOK(req *http.Request) bool {
	if s.deps.AcpToken == nil {
		return false
	}
	presented, offered := acpPresented(req.Header, req.URL.Query())
	return acpauth.Equal(s.deps.AcpToken(), presented) && offered
}

// refusalLog writes one line a window and counts the refusals it held back.
type refusalLog struct {
	say   func(string)
	now   func() time.Time
	every time.Duration

	mu   sync.Mutex
	last time.Time
	held int
}

func newRefusalLog(say func(string), now func() time.Time, every time.Duration) *refusalLog {
	return &refusalLog{say: say, now: now, every: every}
}

// refused records one refusal: the line goes out at the first, and again once the window has passed, with the number
// held back since the line before and how long ago that was.
func (l *refusalLog) refused(line string) {
	if l == nil || l.say == nil {
		return
	}
	l.mu.Lock()
	now := l.now()
	if !l.last.IsZero() && now.Sub(l.last) < l.every {
		l.held++
		l.mu.Unlock()
		return
	}
	held, since := l.held, now.Sub(l.last)
	l.last, l.held = now, 0
	l.mu.Unlock()
	if held > 0 {
		line += fmt.Sprintf(" (%d more since the last line, %d s ago)", held, int(since/time.Second))
	}
	l.say(line)
}
