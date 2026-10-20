package goserver

import (
	"strings"
	"testing"
)

// incompatibility is the rule by which an app's server keeps a daemon it cannot serve off its online list.
func TestIncompatibilityIsTheRangeAndWhichSideIsOlder(t *testing.T) {
	for _, c := range []struct {
		name     string
		protocol int
		min, max float64
		want     string // "" is compatible; else a phrase the sentence holds
	}{
		{"the range holds the protocol", 1, 1, 3, ""},
		{"the protocol is the top of the range", 3, 1, 3, ""},
		{"the protocol is the bottom of the range", 2, 2, 4, ""},
		{"a daemon older than the range names none and speaks 1", 1, 0, 0, ""},
		{"a daemon older than the range names none and the app speaks 2", 2, 0, 0, "Update agentd"},
		{"the daemon tops out below the app", 3, 1, 2, "Update agentd"},
		{"the daemon starts above the app", 1, 2, 3, "needs an update"},
		{"a range that is backwards holds nothing, whichever side is blamed", 2, 3, 1, "too old"},
	} {
		got := incompatibility(c.protocol, c.min, c.max)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: incompatibility(%d, %v, %v) = %q, want %q", c.name, c.protocol, c.min, c.max, got, c.want)
		}
	}
}

func TestIncompatibilityNamesBothRanges(t *testing.T) {
	got := incompatibility(1, 2, 3)
	if !strings.Contains(got, "protocol 2-3") || !strings.Contains(got, "protocol 1") {
		t.Errorf("got %q, want it to name the daemon's range 2-3 and the app's protocol 1", got)
	}
	for _, word := range []string{"daemon", "engine", "origin"} {
		if strings.Contains(strings.ToLower(got), word) {
			t.Errorf("the sentence is shown to a person and says %q: %q", word, got)
		}
	}
}
