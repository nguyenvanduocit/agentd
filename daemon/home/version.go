package home

// scripts/build-daemon.sh sets Version, ReleaseKey and ReleaseURL at link time (-ldflags "-X ..."); a source build
// reports "dev", has no release key (so it refuses every update) and no release server.
var (
	// Version is the daemon's version.
	Version = "dev"
	// ReleaseKey is the release public key (base64 of its SPKI DER) an update must be signed with; empty means none.
	ReleaseKey = ""
	// ReleaseURL is the release server (its origin) the daemon takes updates from, unless $AGENTD_RELEASE_URL says another; empty means none.
	ReleaseURL = ""
)

// Protocol is the version of the daemon contract this daemon speaks: an app's `hello` reply must name the same.
const Protocol = 1
