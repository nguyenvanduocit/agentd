// fake-agent is a stand-in for an ACP adapter in the daemon's tests: it answers `initialize` on stdout and ignores
// everything else, so a test can see lines go through the daemon in both directions without a model.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 10<<20)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion json.RawMessage `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.Method != "initialize" {
			continue
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%s}}`+"\n", req.ID, req.Params.ProtocolVersion)
	}
}
