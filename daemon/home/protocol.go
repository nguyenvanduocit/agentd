package home

import (
	"math"
	"strconv"
)

// The protocols of the contract (contract/define.acp.ts) this daemon speaks, oldest and newest. An app's server
// names the protocol it speaks in its answer to hello; the daemon keeps the link when that protocol is in the range,
// and leaves the app when it is not. The daemon sends the range in hello, so a server can tell a daemon it cannot
// serve (and say so on its daemons list) instead of being cut off without a word. A daemon older than the range
// (0.6) sends none and speaks exactly Protocol.
//
// Raise ProtocolMax when the contract gains a protocol this daemon speaks, and ProtocolMin only when it stops
// speaking an old one: every app server that still names that protocol is left from then on.
const (
	ProtocolMin = 1
	ProtocolMax = Protocol
)

// SpeaksProtocol is whether the daemon speaks the protocol an app's server named.
func SpeaksProtocol(protocol float64) bool {
	return protocol == math.Trunc(protocol) && protocol >= ProtocolMin && protocol <= ProtocolMax
}

// ProtocolRangeText is a range of protocols as a person reads it: "1", or "1-3".
func ProtocolRangeText(min, max float64) string {
	if min == max {
		return strconv.FormatFloat(min, 'f', -1, 64)
	}
	return strconv.FormatFloat(min, 'f', -1, 64) + "-" + strconv.FormatFloat(max, 'f', -1, 64)
}
