package cli

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var decimalLiteral = regexp.MustCompile(`^[+-]?(?:\d+\.?\d*(?:[eE][+-]?\d+)?|\.\d+(?:[eE][+-]?\d+)?)$`)

func isJSSpace(r rune) bool {
	return r == 0xFEFF || (r != 0x85 && unicode.IsSpace(r))
}

// jsNumber converts a string the way JavaScript's Number(s) does: surrounding whitespace is ignored, "" is 0,
// 0x/0o/0b prefixes read as integers, "Infinity" is infinite, anything else that is not a decimal literal is NaN.
func jsNumber(s string) float64 {
	s = strings.TrimFunc(s, isJSSpace)
	if s == "" {
		return 0
	}
	if len(s) > 2 && s[0] == '0' {
		base := map[byte]float64{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		if base != 0 {
			return parseRadix(s[2:], base)
		}
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if !decimalLiteral.MatchString(s) {
		return math.NaN()
	}
	// ParseFloat reports an out-of-range value as ±Inf with an error, which is JavaScript's answer too.
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseRadix(digits string, base float64) float64 {
	var v float64
	for _, c := range digits {
		d := float64(digitValue(c))
		if d < 0 || d >= base {
			return math.NaN()
		}
		v = v*base + d
	}
	return v
}

func digitValue(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

// positiveInt is the value of s when it reads as a whole number of at least 1. A count beyond the int range is the largest int.
func positiveInt(s string) (int, bool) {
	f := jsNumber(s)
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 1 {
		return 0, false
	}
	if f >= math.MaxInt64 {
		return math.MaxInt, true
	}
	return int(f), true
}
