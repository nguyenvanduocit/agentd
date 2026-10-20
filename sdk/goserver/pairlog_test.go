package goserver_test

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// A wrong code is logged with the address the rate limit counts it against: behind a proxy, the log shows whether the
// configured header is what the limit sees.
func TestAWrongCodeIsLoggedWithTheAddressItCountsAgainst(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newEnvWith(t, goserver.Options{ClientIPHeader: "X-Test-Client-Ip"})
	e.startCode(t, alice)
	if _, status := e.postPairFrom(t, "AAAAA-AAAAA", "laptop", "2001:db8:1:2:3:4:5:6"); status != http.StatusForbidden {
		t.Fatalf("a wrong code = %d, want 403", status)
	}
	if got := out.String(); !strings.Contains(got, "wrong pairing code from 2001:db8:1:2::/64 (1 in the last minute)") {
		t.Fatalf("log = %q, want the wrong code and the client it counts against", got)
	}
}
