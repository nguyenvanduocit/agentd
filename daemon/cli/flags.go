package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// This file is the Go form of Node's util.parseArgs with strict: true and allowPositionals: true, for the daemon's
// option table. The argument shapes it accepts and the error messages it gives are Node's, so a mistake reads the
// same as it did when the daemon ran on Node.

type flagKind int

const (
	boolFlag flagKind = iota
	stringFlag
)

type flagSpec struct {
	name  string
	short rune // 0 when the flag has no short form
	kind  flagKind
}

var flagSpecs = []flagSpec{
	{name: "app", kind: stringFlag},
	{name: "dir", kind: stringFlag},
	{name: "config", kind: stringFlag},
	{name: "code", kind: stringFlag},
	{name: "session", kind: stringFlag},
	{name: "page", kind: boolFlag},
	{name: "lines", short: 'n', kind: stringFlag},
	{name: "follow", short: 'f', kind: boolFlag},
	{name: "help", short: 'h', kind: boolFlag},
}

func specByName(name string) (flagSpec, bool) {
	for _, s := range flagSpecs {
		if s.name == name {
			return s, true
		}
	}
	return flagSpec{}, false
}

// longForShort is the flag name behind a short letter; a letter no flag claims names itself, and so is unknown.
func longForShort(short rune) string {
	for _, s := range flagSpecs {
		if s.short == short {
			return s.name
		}
	}
	return string(short)
}

func isStringFlag(short rune) bool {
	s, ok := specByName(longForShort(short))
	return ok && s.kind == stringFlag
}

// flagValues holds what the command line set. A string flag that was not given is nil, so `--dir=` (empty) and no
// `--dir` stay different; the last occurrence of a flag wins.
type flagValues struct {
	app, dir, config, code, session, lines *string
	page, follow, help                     bool
}

func (v *flagValues) store(name string, value *string) {
	switch name {
	case "app":
		v.app = value
	case "dir":
		v.dir = value
	case "config":
		v.config = value
	case "code":
		v.code = value
	case "session":
		v.session = value
	case "lines":
		v.lines = value
	case "page":
		v.page = true
	case "follow":
		v.follow = true
	case "help":
		v.help = true
	}
}

type flagToken struct {
	name    string
	rawName string
	value   *string
	inline  bool // the value came attached (`--app=x`, `-nx`), not as the next argument
}

func isLoneShort(arg string) bool {
	return utf8.RuneCountInString(arg) == 2 && arg[0] == '-' && arg[1] != '-'
}

// valueSeparator is the byte index of the `=` that splits `--name=value`, or -1. Like Node it looks from the fourth
// character on, so `--=x` is a flag named "=x".
func valueSeparator(arg string) int {
	for i, r := range []rune(arg) {
		if i >= 3 && r == '=' {
			return len(string([]rune(arg)[:i]))
		}
	}
	return -1
}

func isLoneLong(arg string) bool {
	return utf8.RuneCountInString(arg) > 2 && strings.HasPrefix(arg, "--") && valueSeparator(arg) < 0
}

func isLongAndValue(arg string) bool {
	return utf8.RuneCountInString(arg) > 2 && strings.HasPrefix(arg, "--") && valueSeparator(arg) >= 0
}

// shortCluster is the letters of a `-xyz` argument: a dash, then more than one rune, the first not a dash.
func shortCluster(arg string) ([]rune, bool) {
	runes := []rune(arg)
	if len(runes) <= 2 || runes[0] != '-' || runes[1] == '-' {
		return nil, false
	}
	return runes[1:], true
}

// tokenize splits the arguments into flag tokens and positionals. Like Node it never fails; checkToken judges the tokens.
func tokenize(args []string) ([]flagToken, []string) {
	var tokens []flagToken
	var positionals []string
	rest := append([]string(nil), args...)
	for len(rest) > 0 {
		arg := rest[0]
		rest = rest[1:]
		if arg == "--" {
			positionals = append(positionals, rest...)
			break
		}
		switch letters, isCluster := shortCluster(arg); {
		case isLoneShort(arg):
			letter := []rune(arg)[1]
			tok := flagToken{name: longForShort(letter), rawName: arg}
			if isStringFlag(letter) && len(rest) > 0 {
				value := rest[0]
				rest = rest[1:]
				tok.value = &value
			}
			tokens = append(tokens, tok)
		case isCluster && !isStringFlag(letters[0]):
			// -fXzy is -f -X -z -y; a string flag inside takes the rest of the cluster as its value (-abfFILE is -a -b -fFILE).
			var expanded []string
			for i, letter := range letters {
				if !isStringFlag(letter) || i == len(letters)-1 {
					expanded = append(expanded, "-"+string(letter))
					continue
				}
				expanded = append(expanded, "-"+string(letters[i:]))
				break
			}
			rest = append(expanded, rest...)
		case isCluster:
			// -nFILE: the first letter is a string flag and the rest is its value.
			value := string(letters[1:])
			tokens = append(tokens, flagToken{name: longForShort(letters[0]), rawName: "-" + string(letters[0]), value: &value, inline: true})
		case isLoneLong(arg):
			tok := flagToken{name: arg[2:], rawName: arg}
			if s, ok := specByName(tok.name); ok && s.kind == stringFlag && len(rest) > 0 {
				value := rest[0]
				rest = rest[1:]
				tok.value = &value
			}
			tokens = append(tokens, tok)
		case isLongAndValue(arg):
			eq := valueSeparator(arg)
			value := arg[eq+1:]
			tokens = append(tokens, flagToken{name: arg[2:eq], rawName: "--" + arg[2:eq], value: &value, inline: true})
		default:
			positionals = append(positionals, arg)
		}
	}
	return tokens, positionals
}

func jsonQuote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `"` + s + `"`
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// checkToken is Node's strict-mode check of one flag token.
func checkToken(tok flagToken) (flagSpec, error) {
	spec, ok := specByName(tok.name)
	if !ok {
		// The closing quote of the hint is missing in Node's own message too.
		return spec, errors.New("Unknown option '" + tok.rawName + "'. To specify a positional argument starting with a '-', place it at the end of the command after '--', as in '-- " + jsonQuote(tok.rawName))
	}
	shortAndLong := "--" + spec.name
	if spec.short != 0 {
		shortAndLong = "-" + string(spec.short) + ", " + shortAndLong
	}
	if spec.kind == stringFlag && tok.value == nil {
		return spec, errors.New("Option '" + shortAndLong + " <value>' argument missing")
	}
	if spec.kind == boolFlag && tok.value != nil {
		return spec, errors.New("Option '" + shortAndLong + "' does not take an argument")
	}
	if !tok.inline && tok.value != nil && utf8.RuneCountInString(*tok.value) > 1 && (*tok.value)[0] == '-' {
		example := "'--" + tok.name + "=-XYZ' or '" + tok.rawName + "-XYZ'"
		if strings.HasPrefix(tok.rawName, "--") {
			example = "'" + tok.rawName + "=-XYZ'"
		}
		return spec, errors.New("Option '" + tok.rawName + "' argument is ambiguous.\nDid you forget to specify the option argument for '" + tok.rawName + "'?\nTo specify an option argument starting with a dash use " + example + ".")
	}
	return spec, nil
}

// parseFlags returns the flag values and the positional arguments, or the first mistake in argument order.
func parseFlags(args []string) (flagValues, []string, error) {
	tokens, positionals := tokenize(args)
	var values flagValues
	for _, tok := range tokens {
		spec, err := checkToken(tok)
		if err != nil {
			return flagValues{}, nil, err
		}
		values.store(spec.name, tok.value)
	}
	return values, positionals, nil
}
