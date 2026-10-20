// Command sign-daemon signs daemon releases with the Ed25519 release key. scripts/build-daemon.sh runs it; the daemon
// checks the signature with daemon/update (SignatureProblem) before it unpacks an update.
//
//	sign-daemon keygen <key>                    a new private key (PKCS8 PEM, mode 0600); an existing one is kept
//	sign-daemon public <key>                    its public key, SPKI PEM (what daemon/release.pub holds)
//	sign-daemon public-b64 <key>                its public key as the daemon bakes it in: base64 of the SPKI DER
//	sign-daemon b64 <public>                    a public key file (daemon/release.pub, daemon/release-backup.pub) as the daemon bakes it in
//	sign-daemon sign <key> <version> <sha256>   the base64 signature latest.json carries, and a newline
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nguyenvanduocit/agentd/daemon/update"
)

const usage = "usage: sign-daemon keygen <key> | public <key> | public-b64 <key> | b64 <public> | sign <key> <version> <sha256>\n"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command; its result is the process exit code (2: wrong usage, 1: the command failed).
func run(args []string, stdout, stderr io.Writer) int {
	out, err := command(args)
	switch {
	case errors.Is(err, errUsage):
		_, _ = io.WriteString(stderr, usage)
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "sign-daemon: %v\n", err)
		return 1
	}
	_, _ = io.WriteString(stdout, out)
	return 0
}

var errUsage = errors.New("usage")

// command returns what the command prints to stdout.
func command(args []string) (string, error) {
	if len(args) < 2 {
		return "", errUsage
	}
	name, file := args[0], args[1]
	switch {
	case name == "keygen" && len(args) == 2:
		return "", keygen(file)
	case name == "public" && len(args) == 2:
		key, err := readPrivateKey(file)
		if err != nil {
			return "", err
		}
		return publicKeyPEM(key)
	case name == "public-b64" && len(args) == 2:
		key, err := readPrivateKey(file)
		if err != nil {
			return "", err
		}
		return publicKeyBase64(key)
	case name == "b64" && len(args) == 2:
		return publicFileBase64(file)
	case name == "sign" && len(args) == 4:
		key, err := readPrivateKey(file)
		if err != nil {
			return "", err
		}
		signature, err := signRelease(key, args[2], args[3])
		return signature + "\n", err
	}
	return "", errUsage
}

// readPrivateKey reads a PKCS8 PEM Ed25519 private key.
func readPrivateKey(file string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s holds no PEM block", file)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s is not a PKCS8 private key: %w", file, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the signing key is %T, not Ed25519", parsed)
	}
	return key, nil
}

// publicKeyDER is the SPKI DER of the key's public half.
func publicKeyDER(key ed25519.PrivateKey) ([]byte, error) {
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("the signing key has no Ed25519 public key")
	}
	return x509.MarshalPKIXPublicKey(pub)
}

// publicKeyPEM is the SPKI PEM public key of a private key.
func publicKeyPEM(key ed25519.PrivateKey) (string, error) {
	der, err := publicKeyDER(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// publicKeyBase64 is base64 of the SPKI DER on one line: what the daemon bakes in (home.ReleaseKey).
func publicKeyBase64(key ed25519.PrivateKey) (string, error) {
	der, err := publicKeyDER(key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// publicFileBase64 reads a public key file, one PUBLIC KEY block of an Ed25519 key and nothing else, and returns the key
// as the daemon bakes it in (home.ReleaseKey): base64 of the SPKI DER on one line.
func publicFileBase64(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" {
		return "", fmt.Errorf("%s holds no PUBLIC KEY PEM block", file)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("%s holds more than one public key", file)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("%s is not a public key: %w", file, err)
	}
	if _, ok := parsed.(ed25519.PublicKey); !ok {
		return "", fmt.Errorf("the public key in %s is %T, not Ed25519", file, parsed)
	}
	return base64.StdEncoding.EncodeToString(block.Bytes), nil
}

// signRelease is the base64 Ed25519 signature of update.ReleaseMessage(version, sha256). It is checked against the
// key's own public key before it is returned.
func signRelease(key ed25519.PrivateKey, version, sha256 string) (string, error) {
	message := update.ReleaseMessage(version, sha256)
	signature := ed25519.Sign(key, message)
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok || !ed25519.Verify(pub, message, signature) {
		return "", errors.New("the signature does not verify with the key's own public key")
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// keygen writes a new private key at file unless one is there. A link from a temporary file makes the key appear whole,
// once, even when two builds race.
func keygen(file string) error {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".*.tmp") // mode 0600, a name of its own per call
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, werr := f.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if err := os.Link(tmp, file); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}
