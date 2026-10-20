package goclient

import (
	"strings"
	"testing"
)

// incompatibility is the rule behind Detected.Incompatible; the exported SupportedProtocols is [1] today, so the
// side that is older than every supported protocol is reached here with other lists.
func TestIncompatibilityTellsWhichSideToUpdate(t *testing.T) {
	for _, c := range []struct {
		name      string
		protocol  int
		supported []int
		want      string
	}{
		{"states none", 0, []int{1}, ""},
		{"supported", 1, []int{1, 2}, ""},
		{"newer than every supported", 3, []int{1, 2}, "newer than this app understands"},
		{"older than every supported", 1, []int{2, 3}, "too old for this app"},
		{"between two supported ones, which is not supported", 2, []int{1, 3}, "newer than this app understands"},
	} {
		got := incompatibility(c.protocol, c.supported)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: incompatibility(%d, %v) = %q, want %q", c.name, c.protocol, c.supported, got, c.want)
		}
	}
}
