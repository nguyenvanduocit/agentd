import { createPublicKey, verify, type KeyObject } from "node:crypto"

/**
 * The bytes a release signature covers: the version and the tarball's sha256, after a prefix that keeps a signature of
 * anything else by the same key from ever verifying here. scripts/sign-daemon.ts signs these bytes.
 */
export function releaseMessage(version: string, sha256: string): Buffer {
  return Buffer.from(`agentd\n${version}\n${sha256}\n`, "utf8")
}

/**
 * An Ed25519 signature as an offer carries it: canonical padded base64 of 64 bytes. Anything else is undefined, a
 * missing field too (a server older than signing sends no `signature`).
 */
export function decodeSignature(text: unknown): Buffer | undefined {
  if (typeof text !== "string") return undefined
  const raw = Buffer.from(text, "base64")
  return raw.length === 64 && raw.toString("base64") === text ? raw : undefined
}

function ed25519Key(base64: string): KeyObject | undefined {
  try {
    const key = createPublicKey({ key: Buffer.from(base64, "base64"), format: "der", type: "spki" })
    return key.asymmetricKeyType === "ed25519" ? key : undefined
  } catch {
    return undefined
  }
}

export interface Signed {
  version: string
  sha256: string
  signature: string
}

/**
 * Why `s` is not signed by the release key; undefined when it is. `key` is the release public key baked into the bundle
 * (base64 of the SPKI DER): without one, every offer is refused.
 */
export function signatureProblem(key: string | undefined, s: Signed): string | undefined {
  if (key === undefined || key === "") return "this daemon was built without a release key"
  const pub = ed25519Key(key)
  if (!pub) return "the release key built into this daemon is not an Ed25519 public key"
  const sig = decodeSignature(s.signature)
  if (!sig) return "the signature is not 64 bytes of base64"
  return verify(null, releaseMessage(s.version, s.sha256), pub, sig) ? undefined : "the signature does not match the release key"
}
