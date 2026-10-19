package home

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	secureSchemes    = map[string]string{"https": "https", "wss": "https"}
	plaintextSchemes = map[string]string{"http": "http", "ws": "http"}
	// defaultPorts are the ports a URL leaves out of its host, by its own scheme.
	defaultPorts = map[string]string{"http": "80", "ws": "80", "https": "443", "wss": "443"}
	hasScheme    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	// Only a name that cannot leave this machine may be reached without TLS (a development server, an end-to-end test).
	loopback = regexp.MustCompile(`(?i)^(localhost|127(\.\d{1,3}){3}|\[::1\])$`)
)

// ServerOrigin is the server's origin as an http(s) URL without a trailing slash. It accepts https and wss for any
// host, and http and ws for a loopback host only: anywhere else they would send the auth token over a plaintext
// connection. A path, query or fragment is dropped because the daemon talks to fixed paths.
func ServerOrigin(raw string) (string, error) {
	notURL := fmt.Errorf(`"%s" is not a URL: use the server address, for example https://app.example.com`, raw)
	text := strings.Trim(raw, " \t\r\n")
	m := hasScheme.FindString(text)
	if m == "" {
		return "", notURL
	}
	scheme := strings.ToLower(strings.TrimSuffix(m, ":"))
	secure, isSecure := secureSchemes[scheme]
	plaintext, isPlaintext := plaintextSchemes[scheme]
	if !isSecure && !isPlaintext {
		return "", fmt.Errorf(`"%s": the server address must start with https://`, raw)
	}
	u, err := url.Parse(text)
	if err != nil || u.Hostname() == "" {
		return "", notURL
	}
	hostname := strings.ToLower(u.Hostname())
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host := hostname
	if port := u.Port(); port != "" && port != defaultPorts[scheme] {
		host += ":" + port
	}
	if isSecure {
		return secure + "://" + host, nil
	}
	if !loopback.MatchString(hostname) {
		return "", fmt.Errorf(`"%s": the server address must use https:// — the auth token must not travel over a plaintext connection`, raw)
	}
	return plaintext + "://" + host, nil
}

// NamespaceURL is the Socket.IO namespace daemons and their MCP bridges connect to.
func NamespaceURL(origin string) string {
	return origin + "/agentd"
}

// PairURL is the pairing endpoint of a server.
func PairURL(origin string) string {
	return origin + "/agentd/pair"
}
