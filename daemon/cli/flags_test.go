package cli

import (
	"reflect"
	"testing"
)

// Each case is the result of node:util parseArgs (strict, allowPositionals) on the same arguments and option table.
func TestParseFlagsMatchesNode(t *testing.T) {
	hint := func(raw string) string {
		return "Unknown option '" + raw + "'. To specify a positional argument starting with a '-', place it at the end of the command after '--', as in '-- \"" + raw + "\""
	}
	ambiguousLong := "Option '--app' argument is ambiguous.\nDid you forget to specify the option argument for '--app'?\nTo specify an option argument starting with a dash use '--app=-XYZ'."
	cases := []struct {
		args        []string
		positionals []string
		err         string
		check       func(flagValues) bool
	}{
		{args: []string{"--nope"}, err: hint("--nope")},
		{args: []string{"--nope=1"}, err: hint("--nope")},
		{args: []string{"-x"}, err: hint("-x")},
		{args: []string{"-xy"}, err: hint("-x")},
		{args: []string{"-hx"}, err: hint("-x")},
		{args: []string{"-ab"}, err: hint("-a")},
		{args: []string{"a", "-x"}, err: hint("-x")},
		{args: []string{"-h", "--nope"}, err: hint("--nope")},
		{args: []string{"--nope", "--app"}, err: hint("--nope")},
		{args: []string{"--APP", "x"}, err: hint("--APP")},
		{args: []string{"---"}, err: hint("---")},
		{args: []string{"--="}, err: hint("--=")},
		{args: []string{"--=x"}, err: hint("--=x")},
		{args: []string{"--a=b=c"}, err: hint("--a")},
		{args: []string{"-f=1"}, err: hint("-=")},
		{args: []string{"--toString"}, err: hint("--toString")},
		{args: []string{"--__proto__"}, err: hint("--__proto__")},
		{args: []string{"--app"}, err: "Option '--app <value>' argument missing"},
		{args: []string{"-n"}, err: "Option '-n, --lines <value>' argument missing"},
		{args: []string{"--app", "-x"}, err: ambiguousLong},
		{args: []string{"--app", "--"}, err: ambiguousLong},
		{args: []string{"-n", "-5"}, err: "Option '-n' argument is ambiguous.\nDid you forget to specify the option argument for '-n'?\nTo specify an option argument starting with a dash use '--lines=-XYZ' or '-n-XYZ'."},
		{args: []string{"--page=1"}, err: "Option '--page' does not take an argument"},
		{args: []string{"--follow=x"}, err: "Option '-f, --follow' does not take an argument"},
		{args: []string{"--help=1"}, err: "Option '-h, --help' does not take an argument"},

		{args: []string{"--app", "-"}, check: func(v flagValues) bool { return eq(v.app, p("-")) }},
		{args: []string{"--app=--x"}, check: func(v flagValues) bool { return eq(v.app, p("--x")) }},
		{args: []string{"--app="}, check: func(v flagValues) bool { return eq(v.app, p("")) }},
		{args: []string{"--app", "x", "--app", "y"}, check: func(v flagValues) bool { return eq(v.app, p("y")) }},
		{args: []string{"-n", "5", "--lines", "6"}, check: func(v flagValues) bool { return eq(v.lines, p("6")) }},
		{args: []string{"-fn", "3"}, check: func(v flagValues) bool { return v.follow && eq(v.lines, p("3")) }},
		{args: []string{"-fh"}, check: func(v flagValues) bool { return v.follow && v.help }},
		{args: []string{"-n3"}, check: func(v flagValues) bool { return eq(v.lines, p("3")) }},
		{args: []string{"-fn3"}, check: func(v flagValues) bool { return v.follow && eq(v.lines, p("3")) }},
		{args: []string{"-nf"}, check: func(v flagValues) bool { return !v.follow && eq(v.lines, p("f")) }},
		{args: []string{"-f-", "a"}, positionals: []string{"a"}, check: func(v flagValues) bool { return v.follow }},
		{args: []string{"-fh-", "x"}, positionals: []string{"x"}, check: func(v flagValues) bool { return v.follow && v.help }},
		{args: []string{"--", "a", "b"}, positionals: []string{"a", "b"}},
		{args: []string{"x", "--", "--y"}, positionals: []string{"x", "--y"}},
		{args: []string{"--app", "x", "--", "y"}, positionals: []string{"y"}, check: func(v flagValues) bool { return eq(v.app, p("x")) }},
		{args: []string{"-"}, positionals: []string{"-"}},
		{args: []string{"-", "x"}, positionals: []string{"-", "x"}},
		{args: []string{"-n", "5", "extra"}, positionals: []string{"extra"}, check: func(v flagValues) bool { return eq(v.lines, p("5")) }},
		{args: []string{"--lines", "  "}, check: func(v flagValues) bool { return eq(v.lines, p("  ")) }},
	}
	for _, c := range cases {
		v, pos, err := parseFlags(c.args)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("parseFlags(%q) error = %v, want %q", c.args, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFlags(%q) error = %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(append([]string(nil), pos...), append([]string(nil), c.positionals...)) {
			t.Errorf("parseFlags(%q) positionals = %q, want %q", c.args, pos, c.positionals)
		}
		if c.check != nil && !c.check(v) {
			t.Errorf("parseFlags(%q) values = %+v", c.args, v)
		}
	}
}

func TestJSONQuote(t *testing.T) {
	if got := jsonQuote(`a"b<c>&\`); got != `"a\"b<c>&\\"` {
		t.Errorf("jsonQuote = %s", got)
	}
}
