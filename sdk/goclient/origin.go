package goclient

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// OriginError is an origin the gateway would turn down. Its message says what to write instead.
type OriginError struct {
	// Origin is what was given.
	Origin string
	// Problem says what is wrong with it and, when there is one, what to use.
	Problem string
}

func (e *OriginError) Error() string {
	if e.Origin == "" {
		return "goclient: " + e.Problem
	}
	return "goclient: origin " + strconv.Quote(e.Origin) + " " + e.Problem
}

// loopbackHost is the hosts that may be reached without TLS: a name that cannot leave this computer (the gateway's own
// rule, daemon/home/server.go).
var loopbackHost = regexp.MustCompile(`(?i)^(localhost|127(\.\d{1,3}){3}|\[::1\])$`)

var defaultPortOf = map[string]string{"http": "80", "https": "443"}

// ValidateOrigin says whether the daemon's gateway would take origin as the app's origin: an https origin, or an http
// one on a loopback host, written as a browser writes it in the Origin header: scheme, lowercase host, a port unless
// it is the scheme's own, and nothing after it (a trailing slash is a path). The error is an *OriginError that names the
// origin to use. A gateway turns down any other with ReasonOrigin, which looks like "no agentd here" to the caller:
// this finds the mistake before the call. Since 0.7.0.
func ValidateOrigin(origin string) error {
	if origin == "" {
		return &OriginError{Problem: `the origin is empty; use the app's origin, for example "https://app.example.com"`}
	}
	bad := func(problem string) error { return &OriginError{Origin: origin, Problem: problem} }
	if strings.ContainsFunc(origin, func(r rune) bool { return r > unicode.MaxASCII }) {
		return bad("is not ASCII; write the host in its punycode form")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return bad(`is not a URL; use for example "https://app.example.com"`)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return bad("must start with https:// (or http:// for localhost)")
	}
	if u.Host == "" {
		return bad(`has no host; use for example "https://app.example.com"`)
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	hostPort := host
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return bad("has a port that is not a number from 1 to 65535")
		}
		if port != defaultPortOf[u.Scheme] {
			hostPort += ":" + port
		}
	}
	want := u.Scheme + "://" + hostPort
	switch {
	case u.User != nil:
		return bad("has a user name; use " + strconv.Quote(want))
	case u.Path != "" || u.Opaque != "":
		return bad("has a path; use " + strconv.Quote(want))
	case u.RawQuery != "" || u.ForceQuery:
		return bad("has a query; use " + strconv.Quote(want))
	case u.Fragment != "" || strings.Contains(origin, "#"):
		return bad("has a fragment; use " + strconv.Quote(want))
	}
	if u.Scheme == "http" && !loopbackHost.MatchString(host) {
		return bad("is plain http, and plain http is accepted for localhost only; use " + strconv.Quote("https://"+hostPort))
	}
	if origin != want {
		return bad("is not in the form a browser sends; use " + strconv.Quote(want))
	}
	return nil
}
