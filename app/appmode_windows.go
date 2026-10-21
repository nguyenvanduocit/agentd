package main

import "log"

// startApp: agentd.app runs on macOS and Linux, where the engine does; on Windows this program is only agentd's command
// line (daemon/entry).
func startApp() bool {
	log.Println("agentd.app does not run on Windows: use agentd's commands")
	return false
}
