package home

// Version is the daemon's version: agentd.app sets it from app/mygo.json (app/version.go); cmd/agentd reports "dev"
// unless the build sets it (-ldflags "-X .../daemon/home.Version=...").
var Version = "dev"

// Protocol is the version of the daemon contract this daemon speaks best. An app's `hello` reply must name a protocol
// in [ProtocolMin, ProtocolMax] (protocol.go).
const Protocol = 1
