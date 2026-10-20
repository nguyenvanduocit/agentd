package link

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/contract"
	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// The daemon tells the app's server which protocols it speaks, so that a server it cannot serve can say so, instead of
// being cut off with the reason only in the daemon's log.
func TestAppHubHelloNamesTheRangeOfProtocolsTheDaemonSpeaks(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	hello := s.last(1).waitEvents("hello", 1)[0]
	var info contract.Hello
	if err := json.Unmarshal(hello.args[0], &info); err != nil {
		t.Fatal(err)
	}
	if info.ProtocolMin != home.ProtocolMin || info.ProtocolMax != home.ProtocolMax {
		t.Errorf("hello names protocols %v-%v, want %d-%d: %s", info.ProtocolMin, info.ProtocolMax, home.ProtocolMin, home.ProtocolMax, hello.args[0])
	}
}

func TestAppHubTheLogNamesTheRangeOfAnAppItLeaves(t *testing.T) {
	s := newFakeServer(t)
	s.set(func() { s.protocol = home.ProtocolMax + 1 })
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	r.waitLog(s.srv.URL + " speaks protocol 2, this daemon speaks " + home.ProtocolRangeText(home.ProtocolMin, home.ProtocolMax) + ": disconnecting it. Update the daemon or the app.")
}

// A call of the server that this daemon does not know used to be dropped without a word, and its caller waited out the
// acknowledgement timeout (90 s for the Go server).
func TestAnUnknownCallOfTheServerIsAnsweredAtOnce(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	c.waitEvents("hello", 1)

	values := c.call("surprise", "x")
	if len(values) != 1 {
		t.Fatalf("answer = %s", values)
	}
	var failure contract.RpcError
	if err := json.Unmarshal(values[0], &failure); err != nil || !failure.RPCError || failure.Code != contract.CodeInvalidArgument || failure.Method != "surprise" {
		t.Fatalf("answer = %s", values[0])
	}
	if !strings.Contains(failure.Message, "does not know surprise") || !strings.Contains(failure.Message, "update agentd") {
		t.Errorf("message = %q, want it to name the call and say what to do", failure.Message)
	}
}

// A call that wants no answer reports its failure on the error event, as the known ones do.
func TestAnUnknownSignalOfTheServerIsReportedOnTheErrorEvent(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	c.waitEvents("hello", 1)

	c.emit("surprise")
	errs := c.waitEvents(contract.RPCErrorEvent, 1)
	var failure contract.RpcError
	if err := json.Unmarshal(errs[0].args[0], &failure); err != nil || failure.Code != contract.CodeInvalidArgument || failure.Method != "surprise" {
		t.Fatalf("error event = %s", errs[0].args[0])
	}
}

// The server's own error event is the library's, not the contract's: answering it would be an answer to an answer.
func TestTheServersErrorEventIsNotAnswered(t *testing.T) {
	s := newFakeServer(t)
	r := newRig(t, echoAgent)
	r.add(s.srv.URL, "tok")
	c := s.last(1)
	c.waitEvents("hello", 1)

	c.emit(contract.RPCErrorEvent, contract.NewRpcError(contract.CodeInternalError, "the server failed", "frame", nil))
	c.call("write", "no-such-session", "x") // answered after the event above was read: the loop reads in order
	if got := c.sent(contract.RPCErrorEvent); len(got) != 0 {
		t.Errorf("the daemon answered the server's error event with %d error event(s)", len(got))
	}
}
