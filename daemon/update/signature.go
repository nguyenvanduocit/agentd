package update

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
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

// SignatureProblem says why s is not signed by the release key; "" when it is. key is the release public key built
// into the binary (base64 of the SPKI DER, home.ReleaseKey): without one, every offer is refused.
func SignatureProblem(key string, s Signed) string {
	if key == "" {
		return "this daemon was built without a release key"
	}
	pub, ok := ed25519Key(key)
	if !ok {
		return "the release key built into this daemon is not an Ed25519 public key"
	}
	sig, ok := DecodeSignature(s.Signature)
	if !ok {
		return "the signature is not 64 bytes of base64"
	}
	if !ed25519.Verify(pub, ReleaseMessage(s.Version, s.SHA256), sig) {
		return "the signature does not match the release key"
	}
	return ""
}
