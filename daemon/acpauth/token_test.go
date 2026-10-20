package acpauth

import (
	"strings"
	"testing"
)

func TestANewTokenIs43CharactersOfBase64URLAndNeverRepeats(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := New()
		if len(tok) != TokenLen || !Valid(tok) {
			t.Fatalf("New() = %q (%d chars), want %d characters of the URL-safe alphabet", tok, len(tok), TokenLen)
		}
		if seen[tok] {
			t.Fatalf("New() repeated %q", tok)
		}
		seen[tok] = true
	}
}

func TestValid(t *testing.T) {
	good := strings.Repeat("a", TokenLen)
	for _, c := range []struct {
		name string
		tok  string
		want bool
	}{
		{"43 letters", good, true},
		{"with - and _", strings.Repeat("-_", 21) + "A", true},
		{"empty", "", false},
		{"one short", good[1:], false},
		{"one long", good + "a", false},
		{"a space inside", good[:20] + " " + good[21:], false},
		{"a newline at the end", good[1:] + "\n", false},
		{"a plus (standard base64, not URL-safe)", good[:20] + "+" + good[21:], false},
		{"a slash", good[:20] + "/" + good[21:], false},
		{"padding", good[1:] + "=", false},
		{"not ASCII", good[:20] + "é" + good[22:], false},
	} {
		if got := Valid(c.tok); got != c.want {
			t.Errorf("%s: Valid(%q) = %v, want %v", c.name, c.tok, got, c.want)
		}
	}
}

func TestEqual(t *testing.T) {
	want := New()
	other := New()
	for _, c := range []struct {
		name      string
		want, got string
		equal     bool
	}{
		{"the same token", want, want, true},
		{"another token", want, other, false},
		{"the last character differs", want, want[:TokenLen-1] + flip(want[TokenLen-1]), false},
		{"the first character differs", want, flip(want[0]) + want[1:], false},
		{"nothing presented", want, "", false},
		{"a truncated token", want, want[:TokenLen-1], false},
		{"a longer value that starts with the token", want, want + "x", false},
		{"a leading space", want, " " + want, false},
		{"a trailing space", want, want + " ", false},
		{"the token expected is empty", "", "", false},
		{"the token expected is not a token: even the same text is refused", "short", "short", false},
	} {
		if got := Equal(c.want, c.got); got != c.equal {
			t.Errorf("%s: Equal(%q, %q) = %v, want %v", c.name, c.want, c.got, got, c.equal)
		}
	}
}

// flip is another valid token character than c.
func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
