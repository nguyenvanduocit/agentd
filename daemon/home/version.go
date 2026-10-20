package home

// scripts/build-daemon.sh sets Version, ReleaseKey and ReleaseURL at link time (-ldflags "-X ..."); a source build
// reports "dev", has no release key (so it refuses every update) and no release server.
var (
	// Version is the daemon's version.
	Version = "dev"
	// ReleaseKey is the release public key set: the base64 of the SPKI DER of each key, separated by commas (the key the
	// releases are signed with, and a backup key). An update must be signed by one of them; empty means none.
	ReleaseKey = ""
	// ReleaseURL is the release server (its origin) the daemon takes updates from, unless $AGENTD_RELEASE_URL says another; empty means none.
	ReleaseURL = ""
)

// Protocol is the version of the daemon contract this daemon speaks best. An app's `hello` reply must name a protocol
// in [ProtocolMin, ProtocolMax] (protocol.go).
const Protocol = 1
