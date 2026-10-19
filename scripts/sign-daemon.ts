// Signs daemon releases with the Ed25519 release key. scripts/build-daemon.sh runs it; the daemon checks the signature
// with daemon/src/core/signature.ts before it unpacks an update.
//
//   bun scripts/sign-daemon.ts keygen <key>                    a new private key (PKCS8 PEM, mode 0600); an existing one is kept
//   bun scripts/sign-daemon.ts public <key>                    its public key, SPKI PEM (what daemon/release.pub holds)
//   bun scripts/sign-daemon.ts public-b64 <key>                its public key as the bundle bakes it in: base64 of the SPKI DER
//   bun scripts/sign-daemon.ts sign <key> <version> <sha256>   the base64 signature install.json carries
import { createPrivateKey, createPublicKey, generateKeyPairSync, sign, verify, type KeyObject } from "node:crypto"
import { linkSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { releaseMessage } from "../daemon/src/core/signature.ts"

function ed25519Private(pem: string): KeyObject {
  const key = createPrivateKey(pem)
  if (key.asymmetricKeyType !== "ed25519") throw new Error(`the signing key is ${String(key.asymmetricKeyType)}, not Ed25519`)
  return key
}

/** The SPKI PEM public key of a PKCS8 PEM Ed25519 private key. */
export function publicKeyPem(privatePem: string): string {
  return createPublicKey(ed25519Private(privatePem)).export({ type: "spki", format: "pem" }).toString()
}

/** The body of an SPKI PEM, base64 of the DER on one line: what the bundle bakes in. */
export function publicKeyBase64(pem: string): string {
  return createPublicKey(pem).export({ type: "spki", format: "der" }).toString("base64")
}

/** The base64 Ed25519 signature of releaseMessage(version, sha256). It is checked against the public key before it is returned. */
export function signRelease(privatePem: string, version: string, sha256: string): string {
  const key = ed25519Private(privatePem)
  const message = releaseMessage(version, sha256)
  const signature = sign(null, message, key)
  if (!verify(null, message, createPublicKey(key), signature)) throw new Error("the signature does not verify with the key's own public key")
  return signature.toString("base64")
}

/** Writes a new private key at `file` unless one is there. A link from a temporary file makes the key appear whole, once, even when two builds race. */
function keygen(file: string): void {
  const tmp = `${file}.${process.pid}.tmp`
  writeFileSync(tmp, generateKeyPairSync("ed25519").privateKey.export({ type: "pkcs8", format: "pem" }), { mode: 0o600 })
  try {
    linkSync(tmp, file)
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "EEXIST") throw e
  } finally {
    rmSync(tmp, { force: true })
  }
}

function main(args: string[]): void {
  const [cmd, file, version, sha256] = args
  if (cmd === "keygen" && file !== undefined) keygen(file)
  else if (cmd === "public" && file !== undefined) process.stdout.write(publicKeyPem(readFileSync(file, "utf8")))
  else if (cmd === "public-b64" && file !== undefined) process.stdout.write(publicKeyBase64(publicKeyPem(readFileSync(file, "utf8"))))
  else if (cmd === "sign" && file !== undefined && version !== undefined && sha256 !== undefined) {
    process.stdout.write(`${signRelease(readFileSync(file, "utf8"), version, sha256)}\n`)
  } else {
    process.stderr.write("usage: sign-daemon.ts keygen <key> | public <key> | public-b64 <key> | sign <key> <version> <sha256>\n")
    process.exit(2)
  }
}

if (import.meta.main) main(process.argv.slice(2))
