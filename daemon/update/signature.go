package update

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"strings"
)

// ReleaseMessage is the bytes a release signature covers: the version and the tarball's sha256, after a prefix that
// keeps a signature of anything else by the same key from ever verifying here. cmd/sign-daemon signs these bytes.
func ReleaseMessage(version, sha256 string) []byte {
	return []byte("agentd\n" + version + "\n" + sha256 + "\n")
}

// DecodeSignature reads an Ed25519 signature as an offer carries it: canonical padded base64 of 64 bytes. Anything
// else is false, an empty field too (a server older than signing sends no `signature`).
func DecodeSignature(text string) ([]byte, bool) {
	raw, err := base64.StdEncoding.DecodeString(text)
	// Go's decoder skips newlines: the canonical form is what must come back out.
	if err != nil || len(raw) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(raw) != text {
		return nil, false
	}
	return raw, true
}

func ed25519Key(b64 string) (ed25519.PublicKey, bool) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, false
	}
	pub, ok := key.(ed25519.PublicKey)
	return pub, ok
}

// Signed is what a release signature covers.
type Signed struct {
	Version   string
	SHA256    string
	Signature string
}

// releaseKeys reads the release key set built into the binary (home.ReleaseKey): the base64 of the SPKI DER of each key,
// separated by commas. Spaces around a key and empty entries are ignored; malformed is true when an entry is not an
// Ed25519 public key.
func releaseKeys(list string) (keys []ed25519.PublicKey, malformed bool) {
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		pub, ok := ed25519Key(entry)
		if !ok {
			return nil, true
		}
		keys = append(keys, pub)
	}
	return keys, false
}

// SignatureProblem says why s is not signed by the release key; "" when it is. keys is the release key set built into
// the binary (home.ReleaseKey): the key the releases are signed with, and a backup key kept offline, so that a lost or
// leaked primary key can be replaced by a release the backup key signs. A signature of any key of the set is good. A
// binary without a key refuses every offer, and so does one whose set holds an entry that is not an Ed25519 public key.
func SignatureProblem(keys string, s Signed) string {
	set, malformed := releaseKeys(keys)
	if malformed {
		return "the release key built into this daemon is not an Ed25519 public key"
	}
	if len(set) == 0 {
		return "this daemon was built without a release key"
	}
	sig, ok := DecodeSignature(s.Signature)
	if !ok {
		return "the signature is not 64 bytes of base64"
	}
	message := ReleaseMessage(s.Version, s.SHA256)
	for _, pub := range set {
		if ed25519.Verify(pub, message, sig) {
			return ""
		}
	}
	if len(set) == 1 {
		return "the signature does not match the release key"
	}
	return "the signature does not match any of the release keys"
}
