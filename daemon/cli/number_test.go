package cli

import (
	"math"
	"testing"
)

// The expected values are what JavaScript's Number(s) returns.
func TestJSNumber(t *testing.T) {
	cases := map[string]float64{
		"": 0, "  ": 0, "5": 5, " 5 ": 5, "+5": 5, "-5": -5, "5.": 5, ".5": 0.5, "1e2": 100, "1E-2": 0.01,
		"0x10": 16, "0XfF": 255, "0o17": 15, "0b101": 5, "\uFEFF7\u00A0": 7,
		"Infinity": math.Inf(1), "-Infinity": math.Inf(-1), "1e999": math.Inf(1),
	}
	for in, want := range cases {
		if got := jsNumber(in); got != want {
			t.Errorf("jsNumber(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"x", "1_0", "0x", "0xg", "-0x10", "+0x10", "1e", "e5", "--5", "5 5", "Inf", "infinity", "nan", "\u00855", "1,5", "0b2"} {
		if got := jsNumber(in); !math.IsNaN(got) {
			t.Errorf("jsNumber(%q) = %v, want NaN", in, got)
		}
	}
}
