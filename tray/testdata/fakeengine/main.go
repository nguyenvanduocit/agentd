// Command fakeengine speaks the engine's side of the app's channel on fd 3 (AGENTD_UI_FD): it sends a state, appends
// each choice it gets to the file $FAKE_ENGINE_GOT, exits 75 when told "exit75", and exits 0 when the channel closes,
// unless $FAKE_ENGINE_STUBBORN is set (then only a signal ends it).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	channel := os.NewFile(3, "agentd-ui-engine")
	fmt.Fprintln(channel, `{"t":"state","version":"9.9.9","apps":["https://a"],"pages":[],"sessions":1}`)
	in := bufio.NewScanner(channel)
	for in.Scan() {
		line := in.Text()
		if got, err := os.OpenFile(os.Getenv("FAKE_ENGINE_GOT"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintln(got, line)
			got.Close()
		}
		if strings.Contains(line, "exit75") {
			os.Exit(75)
		}
	}
	if os.Getenv("FAKE_ENGINE_STUBBORN") != "" {
		for {
			time.Sleep(time.Hour)
		}
	}
}
