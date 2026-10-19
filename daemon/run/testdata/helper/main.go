// Command helper holds the agents the daemon's tests run besides shell scripts.
//
//	helper flood <report>   writes 64 KiB lines as fast as its stdout takes them; every 50 ms it saves its pid and the
//	                        bytes the pipe has taken so far to <report>: the daemon holds at most that much of its output
//	helper mcp-agent        starts MCP servers as an ACP agent does from session/new: a line {"start": <server>} runs that
//	                        stdio server with the agent's environment plus the server's env; every other line goes to the
//	                        server's stdin, the server's stdout comes out as the agent's, and its exit is the line
//	                        {"bridgeExit": <code>}
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "flood":
		flood(os.Args[2])
	case "mcp-agent":
		mcpAgent()
	default:
		os.Exit(2)
	}
}

func flood(report string) {
	line := []byte(strings.Repeat("x", 65535) + "\n")
	var flushed atomic.Int64
	go func() {
		for range time.Tick(50 * time.Millisecond) {
			_ = os.WriteFile(report, []byte(fmt.Sprintf("%d %d", os.Getpid(), flushed.Load())), 0o600)
		}
	}()
	for {
		n, err := os.Stdout.Write(line)
		flushed.Add(int64(n))
		if err != nil {
			return
		}
	}
}

type server struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
}

func mcpAgent() {
	var stdin interface{ Write([]byte) (int, error) }
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 10<<20)
	for in.Scan() {
		line := in.Text()
		var m struct {
			Start *server `json:"start"`
		}
		if json.Unmarshal([]byte(line), &m) != nil || m.Start == nil {
			if stdin != nil {
				_, _ = stdin.Write([]byte(line + "\n"))
			}
			continue
		}
		cmd := exec.Command(m.Start.Command, m.Start.Args...)
		cmd.Env = os.Environ()
		for _, e := range m.Start.Env {
			cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
		}
		cmd.Stderr = os.Stderr
		pipe, err := cmd.StdinPipe()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		stdin = pipe
		out, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		go func() {
			lines := bufio.NewScanner(out)
			lines.Buffer(make([]byte, 0, 64*1024), 10<<20)
			for lines.Scan() {
				fmt.Println(lines.Text())
			}
			_ = cmd.Wait()
			fmt.Printf("{\"bridgeExit\":%d}\n", cmd.ProcessState.ExitCode())
		}()
	}
}
