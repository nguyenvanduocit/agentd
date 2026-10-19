package main

import "log"

// startApp: agentd.app runs on macOS and Linux, where the engine does; on Windows this program is only the tray of a
// daemon (`agentd-tray child`).
func startApp() bool {
	log.Println("agentd.app does not run on Windows: start agentd-tray child from a daemon")
	return false
}
