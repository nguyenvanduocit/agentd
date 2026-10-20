package ui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

// The consent window: when the app or the tray program that shows the daemon's state can also ask the user, the daemon
// asks there (one window: what the origin wants, the folders to choose, Allow or Deny) instead of in the native dialogs.
// The wire is the tray's: JSON lines.
//
//	daemon -> UI  {"t":"consent","id","origin","mode","agents":[...],"title","body","timeoutS"}  show the window
//	daemon -> UI  {"t":"consent-end","id"}                                                       close it, no answer wanted
//	UI -> daemon  {"t":"consent-shown","id"}                                                     the window is up
//	UI -> daemon  {"t":"consent-answer","id","answer":"allow"|"deny","folders":[...]}            the user's decision
//
// A UI that does not know "consent" never says "consent-shown", and the daemon asks in the native dialogs instead.

// ackWait is how long a UI has to say its window is up before the daemon asks somewhere else.
const ackWait = 2 * time.Second

type consentRequest struct {
	T        string   `json:"t"`
	ID       string   `json:"id"`
	Origin   string   `json:"origin"`
	Mode     string   `json:"mode"`
	Agents   []string `json:"agents"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	TimeoutS int      `json:"timeoutS"`
}

type consentEnd struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

type replyKind int

const (
	replyShown replyKind = iota + 1
	replyAnswer
	// replyGone is not on the wire: the channel to the UI closed.
	replyGone
)

// Reply is what the UI says of a request.
type Reply struct {
	Kind    replyKind
	ID      string
	Answer  string
	Folders []string
}

// ParseReply reads a line of the UI that answers a consent request; false for any other line.
func ParseReply(line string) (Reply, bool) {
	var m struct {
		T       string   `json:"t"`
		ID      string   `json:"id"`
		Answer  string   `json:"answer"`
		Folders []string `json:"folders"`
	}
	if json.Unmarshal([]byte(line), &m) != nil || m.ID == "" {
		return Reply{}, false
	}
	switch m.T {
	case "consent-shown":
		return Reply{Kind: replyShown, ID: m.ID}, true
	case "consent-answer":
		return Reply{Kind: replyAnswer, ID: m.ID, Answer: m.Answer, Folders: m.Folders}, true
	}
	return Reply{}, false
}

// Asker is a Link that can ask the user: the consent window of the app or the tray program.
type Asker interface {
	// Ask shows the question to the user and waits at most timeoutS for the decision. False means there was no one
	// to ask (no UI, one that does not show the window, or one that went away): the caller asks another way.
	Ask(q consent.Question, timeoutS int) (consent.Decision, bool)
}

// Relay asks the UI at the other end of a line-based channel. It is what a Link asks through.
type Relay struct {
	send    func(line string) bool
	ackWait time.Duration

	mu      sync.Mutex
	pending map[string]chan Reply
}

// NewRelay makes a relay that writes its lines with send, which reports whether there was a UI to write to.
func NewRelay(send func(line string) bool) *Relay {
	return &Relay{send: send, ackWait: ackWait, pending: map[string]chan Reply{}}
}

// Deliver hands a reply to the request it answers; a reply to a request that is gone is dropped.
func (r *Relay) Deliver(rp Reply) {
	r.mu.Lock()
	ch := r.pending[rp.ID]
	r.mu.Unlock()
	if ch != nil {
		select {
		case ch <- rp:
		default: // a UI that talks more than it should
		}
	}
}

// Drop ends every wait: the channel to the UI closed.
func (r *Relay) Drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, ch := range r.pending {
		select {
		case ch <- Reply{Kind: replyGone, ID: id}:
		default:
		}
	}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails on the platforms agentd runs on
	return hex.EncodeToString(b)
}

func line(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Ask shows q in the UI's window. The id of a request is a secret of this daemon and the UI: only a reply that carries
// it is heard.
func (r *Relay) Ask(q consent.Question, timeoutS int) (consent.Decision, bool) {
	id := newID()
	ch := make(chan Reply, 4)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()

	text := consent.ConsentText(q)
	agents := q.Agents
	if agents == nil {
		agents = []string{}
	}
	if !r.send(line(consentRequest{T: "consent", ID: id, Origin: q.Origin, Mode: string(q.Mode), Agents: agents, Title: text.Title, Body: text.Body, TimeoutS: timeoutS})) {
		return consent.Decision{}, false
	}
	end := func() { r.send(line(consentEnd{T: "consent-end", ID: id})) }

	ack := time.NewTimer(r.ackWait)
	defer ack.Stop()
	deadline := time.NewTimer(time.Duration(timeoutS) * time.Second)
	defer deadline.Stop()
	for {
		select {
		case rp := <-ch:
			switch rp.Kind {
			case replyShown:
				ack.Stop()
			case replyGone:
				return consent.Decision{}, false
			case replyAnswer:
				return decisionOf(rp), true
			}
		case <-ack.C:
			end()
			return consent.Decision{}, false
		case <-deadline.C:
			end()
			return consent.Decision{Answer: consent.AnswerTimeout}, true
		}
	}
}

// decisionOf is what a reply decides: Allow needs at least one valid folder, anything else is a Deny.
func decisionOf(rp Reply) consent.Decision {
	if rp.Answer == "allow" {
		if folders := consent.CleanFolders(rp.Folders); len(folders) > 0 {
			return consent.Decision{Answer: consent.AnswerAllow, Folders: folders}
		}
	}
	return consent.Decision{Answer: consent.AnswerDeny}
}
