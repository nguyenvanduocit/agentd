package home

import "testing"

func TestSpeaksProtocolIsTheRangeThisDaemonSpeaks(t *testing.T) {
	for _, c := range []struct {
		protocol float64
		want     bool
	}{
		{ProtocolMin, true},
		{ProtocolMax, true},
		{ProtocolMin - 1, false},
		{ProtocolMax + 1, false},
		{ProtocolMin + 0.5, false}, // a protocol is a whole number
		{0, false},                 // what a server that names none decodes to
		{-1, false},
	} {
		if got := SpeaksProtocol(c.protocol); got != c.want {
			t.Errorf("SpeaksProtocol(%v) = %v, want %v", c.protocol, got, c.want)
		}
	}
}

func TestProtocolRangeIsAWholeNumberRangeThatHoldsTheCurrentProtocol(t *testing.T) {
	if ProtocolMin < 1 || ProtocolMin > ProtocolMax || Protocol < ProtocolMin || Protocol > ProtocolMax {
		t.Fatalf("protocol %d is not in [%d, %d]", Protocol, ProtocolMin, ProtocolMax)
	}
}

func TestProtocolRangeText(t *testing.T) {
	for _, c := range []struct {
		min, max float64
		want     string
	}{
		{1, 1, "1"},
		{1, 3, "1-3"},
		{2, 2, "2"},
	} {
		if got := ProtocolRangeText(c.min, c.max); got != c.want {
			t.Errorf("ProtocolRangeText(%v, %v) = %q, want %q", c.min, c.max, got, c.want)
		}
	}
}
