package goserver

import (
	"fmt"
	"strconv"
)

// incompatibility is why an app that speaks protocol cannot serve a daemon whose hello names the protocols min-max, as a
// sentence for the person who reads the daemons list; "" when it can. A daemon older than the range (0.6) names none, and
// speaks exactly protocol 1.
func incompatibility(protocol int, min, max float64) string {
	if min == 0 && max == 0 {
		min, max = 1, 1
	}
	p := float64(protocol)
	if p >= min && p <= max {
		return ""
	}
	theirs := rangeText(min, max)
	if p > max {
		return fmt.Sprintf("The agentd on this computer is too old for this app: it speaks protocol %s and the app speaks protocol %d. Update agentd on this computer.", theirs, protocol)
	}
	return fmt.Sprintf("This app is too old for the agentd on this computer: it speaks protocol %s and the app speaks protocol %d. The app needs an update.", theirs, protocol)
}

// rangeText is a range of protocols as a person reads it: "1", or "1-3".
func rangeText(min, max float64) string {
	low := strconv.FormatFloat(min, 'f', -1, 64)
	if min == max {
		return low
	}
	return low + "-" + strconv.FormatFloat(max, 'f', -1, 64)
}

// refused is what the server remembers of a daemon whose hello it could not accept, beyond its connection: the daemon
// leaves an app it cannot serve, and the list still has to say why that daemon is not online.
type refused struct {
	reason, version, hostname, os string
}
