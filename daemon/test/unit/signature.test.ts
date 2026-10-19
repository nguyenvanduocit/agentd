import { describe, expect, test } from "bun:test"
import { generateKeyPairSync } from "node:crypto"
import { publicKeyBase64, publicKeyPem, signRelease } from "../../../scripts/sign-daemon.ts"
import { decodeSignature, releaseMessage, signatureProblem } from "../../src/core/signature.ts"

function keyPair(): { pem: string; key: string } {
  const pem = generateKeyPairSync("ed25519").privateKey.export({ type: "pkcs8", format: "pem" }).toString()
  return { pem, key: publicKeyBase64(publicKeyPem(pem)) }
}

const release = keyPair()
const SHA = "ab".repeat(32)
const signed = (version: string, sha256: string, pem = release.pem) => ({ version, sha256, signature: signRelease(pem, version, sha256) })

describe("signatureProblem", () => {
  test("the release key's signature of the version and sha256 verifies", () => {
    expect(signatureProblem(release.key, signed("2.0.0", SHA))).toBeUndefined()
  })

  test("a signature does not carry over to another sha256 (another tarball)", () => {
    const s = signed("2.0.0", SHA)
    expect(signatureProblem(release.key, { ...s, sha256: "cd".repeat(32) })).toContain("does not match")
  })

  test("a signature does not carry over to another version", () => {
    const s = signed("2.0.0", SHA)
    expect(signatureProblem(release.key, { ...s, version: "2.0.1" })).toContain("does not match")
  })

  test("a missing or malformed signature is refused", () => {
    for (const signature of ["", "AAAA", "A".repeat(88), `${signed("2.0.0", SHA).signature.slice(0, 86)}=`]) {
      expect(signatureProblem(release.key, { version: "2.0.0", sha256: SHA, signature })).toBeDefined()
    }
  })

  test("a signature by another key is refused", () => {
    expect(signatureProblem(release.key, signed("2.0.0", SHA, keyPair().pem))).toContain("does not match")
  })

  test("a daemon built without a release key refuses every offer", () => {
    expect(signatureProblem(undefined, signed("2.0.0", SHA))).toContain("without a release key")
    expect(signatureProblem("", signed("2.0.0", SHA))).toContain("without a release key")
  })

  test("a baked key that is not an Ed25519 public key refuses every offer", () => {
    const rsa = generateKeyPairSync("rsa", { modulusLength: 2048 }).publicKey.export({ type: "spki", format: "der" }).toString("base64")
    expect(signatureProblem(rsa, signed("2.0.0", SHA))).toContain("not an Ed25519 public key")
    expect(signatureProblem("bm90IGEga2V5", signed("2.0.0", SHA))).toContain("not an Ed25519 public key")
  })
})

describe("releaseMessage", () => {
  test("is domain separated, one field per line", () => {
    expect(releaseMessage("2.0.0", SHA).toString("utf8")).toBe(`agentd\n2.0.0\n${SHA}\n`)
  })
})

describe("decodeSignature", () => {
  test("only canonical padded base64 of 64 bytes is a signature", () => {
    const good = signed("2.0.0", SHA).signature
    expect(decodeSignature(good)?.length).toBe(64)
    expect(decodeSignature(good.slice(0, -2))).toBeUndefined() // unpadded
    expect(decodeSignature(` ${good}`)).toBeUndefined()
    expect(decodeSignature(Buffer.alloc(63).toString("base64"))).toBeUndefined()
    expect(decodeSignature(Buffer.alloc(65).toString("base64"))).toBeUndefined()
    expect(decodeSignature(undefined)).toBeUndefined()
    expect(decodeSignature(null)).toBeUndefined()
  })
})

describe("sign-daemon", () => {
  test("the public key of a private key is the SPKI PEM the release key file holds", () => {
    expect(publicKeyPem(release.pem)).toMatch(/^-----BEGIN PUBLIC KEY-----\n[A-Za-z0-9+/=]{60}\n-----END PUBLIC KEY-----\n$/)
  })

  test("a signature is 64 bytes of base64", () => {
    expect(decodeSignature(signed("2.0.0", SHA).signature)).toBeDefined()
  })
})

describe("sign-daemon refuses a key that is not Ed25519", () => {
  test("an RSA private key signs nothing", () => {
    const rsa = generateKeyPairSync("rsa", { modulusLength: 2048 }).privateKey.export({ type: "pkcs8", format: "pem" }).toString()
    expect(() => signRelease(rsa, "2.0.0", SHA)).toThrow("not Ed25519")
    expect(() => publicKeyPem(rsa)).toThrow("not Ed25519")
  })
})
