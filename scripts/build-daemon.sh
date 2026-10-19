#!/bin/bash
# Builds the user-machine daemon: a one-file Node bundle and its launcher packed as an npm tarball, plus the index the server serves.
#
#   scripts/build-daemon.sh <outdir>
#
# Writes the files of a GitHub release into <outdir>/download/v<version>/: agentd-<version>.tgz, latest.json ({version, url,
# sha256, signature}) and install.sh (daemon/install.sh.in with this release's values). <outdir>/latest holds the version, which
# cmd/agentd-server (the local stand-in for GitHub Releases) serves as the latest release. The version is
# $VERSION (RELEASE=1: required, x.y.z, and an existing tarball of it is never overwritten), or 0.0.0-g<git describe> (a semver prerelease; the "g" keeps an all-digit hash from reading as a numeric
# identifier with leading zeros, which semver forbids), or 0.0.0-dev when there is no git history. Run the result with: npx -y <tgz> version
#
# RELEASE_URL is the releases page the daemon takes updates from and install.sh downloads from, baked in:
# https://github.com/<owner>/<repo>/releases (the default of a RELEASE=1 build), or the local stand-in's origin in development.
#
# Every build is signed (scripts/sign-daemon.ts): `signature` is the Ed25519 signature of the version and the tarball's
# sha256, and the bundle bakes in the public key a daemon checks every update against. The private key is the file
# $DAEMON_SIGNING_KEY (PKCS8 PEM). Without it the dev key .dev-signing/daemon-signing.key is used, made on first use:
# daemons built with it accept only updates built with it. RELEASE=1 (the Dockerfile) requires $DAEMON_SIGNING_KEY and
# fails unless its public key is daemon/release.pub, so a wrong key never ships daemons that installed ones refuse.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:?usage: scripts/build-daemon.sh <outdir>}"

# A build context without .git (the Docker daemon stage, a source tarball) has no describe: 0.0.0-dev.
if [ -z "${VERSION:-}" ]; then
  if DESCRIBE="$(git -C "$ROOT" describe --always --dirty 2>/dev/null)"; then
    VERSION="0.0.0-g$(printf '%s' "$DESCRIBE" | sed 's/[^0-9A-Za-z.-]/-/g')"
  else
    VERSION="0.0.0-dev"
  fi
else
  # The shape the daemon (VERSION_PATTERN) and the release server (versionPattern) accept: anything else would be signed
  # and published but refused by every daemon, and it is embedded in --define.
  if ! [[ "$VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z.-]{0,63}$ ]] || [[ "$VERSION" =~ ^(current|previous|rejected|crashes)$ ]]; then
    echo "VERSION \"$VERSION\" is not a plain version: [0-9A-Za-z.-], at most 64, starting with a letter or digit" >&2
    exit 1
  fi
fi
# A release is a version number someone chose: a derived 0.0.0-g<hash> would ship as the update every daemon takes.
if [ "${RELEASE:-}" = 1 ] && ! [[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "RELEASE=1 needs VERSION=x.y.z (got \"$VERSION\")" >&2
  exit 1
fi
SIGN=(bun "$ROOT/scripts/sign-daemon.ts")
if [ -n "${DAEMON_SIGNING_KEY:-}" ]; then
  KEY="$DAEMON_SIGNING_KEY"
elif [ "${RELEASE:-}" = 1 ]; then
  echo "RELEASE=1 needs DAEMON_SIGNING_KEY, the path of the release private key" >&2
  exit 1
else
  KEY="$ROOT/.dev-signing/daemon-signing.key"
  if [ ! -f "$KEY" ]; then
    mkdir -p "$(dirname "$KEY")"
    "${SIGN[@]}" keygen "$KEY"
    echo "made the dev signing key $KEY" >&2
  fi
fi
if [ ! -f "$KEY" ] || [ ! -r "$KEY" ]; then
  echo "the signing key $KEY is not a readable file" >&2
  exit 1
fi
PUB="$("${SIGN[@]}" public "$KEY")"
if [ "${RELEASE:-}" = 1 ] && ! cmp -s <(printf '%s\n' "$PUB") "$ROOT/daemon/release.pub"; then
  echo "RELEASE=1: the public key of DAEMON_SIGNING_KEY is not daemon/release.pub; installed daemons would refuse this build" >&2
  exit 1
fi
RELEASE_KEY="$("${SIGN[@]}" public-b64 "$KEY")"

# The releases page the daemon takes updates from, baked into the bundle (AGENTD_RELEASE_URL overrides it at run time).
# A release says it: a daemon that points at a development server would never update.
if [ -z "${RELEASE_URL:-}" ]; then
  if [ "${RELEASE:-}" = 1 ]; then
    RELEASE_URL="https://github.com/nguyenvanduocit/agentd/releases"
  else
    RELEASE_URL="http://127.0.0.1:7100"
  fi
fi
releases_pattern='^https?://[][A-Za-z0-9.:-]+(/[A-Za-z0-9._-]+)*$'
if ! [[ "$RELEASE_URL" =~ $releases_pattern ]] || [[ "$RELEASE_URL" =~ /\.\.?(/|$) ]] || { [ "${RELEASE:-}" = 1 ] && ! [[ "$RELEASE_URL" =~ ^https:// ]]; }; then
  echo "RELEASE_URL \"$RELEASE_URL\" is not a releases page (https://host[:port][/path], no query; a release needs https)" >&2
  exit 1
fi

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
TARBALL="agentd-$VERSION.tgz"
RELEASE_DIR="$OUT/download/v$VERSION"
mkdir -p "$RELEASE_DIR"
# A published version keeps its bytes: daemons record its sha256, and a second build never packs the same bytes.
if [ "${RELEASE:-}" = 1 ] && [ -e "$RELEASE_DIR/$TARBALL" ]; then
  echo "RELEASE=1: $RELEASE_DIR/$TARBALL already exists; a released version is never rebuilt, bump VERSION" >&2
  exit 1
fi

[ -d "$ROOT/daemon/node_modules" ] || (cd "$ROOT/daemon" && bun install --frozen-lockfile)

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
PKG="$STAGE/package"
mkdir "$PKG"

# CommonJS: Node 20 runs daemon.js without a package.json "type" next to it (dist/daemon.js alone, or in the tarball).
bun build "$ROOT/daemon/src/main.ts" --target=node --format=cjs \
  --define "AGENTD_VERSION=\"$VERSION\"" --define "AGENTD_RELEASE_KEY=\"$RELEASE_KEY\"" --define "AGENTD_RELEASE_URL=\"$RELEASE_URL\"" --outfile "$PKG/daemon.js"
chmod 755 "$PKG/daemon.js"
# The launcher install.sh puts at ~/.agentd/bin/agentd; the daemon refreshes it from here after an update.
cp "$ROOT/daemon/launcher.sh" "$PKG/launcher.sh"
chmod 755 "$PKG/launcher.sh"
# The icons of the app that starts agentd again after a Stop (agentd.app, the desktop entry): drawn by tray/icon/gen.go.
mkdir "$PKG/app"
cp "$ROOT/daemon/app/agentd.icns" "$ROOT/daemon/app/agentd.png" "$PKG/app/"

cat >"$PKG/package.json" <<JSON
{
  "name": "agentd",
  "version": "$VERSION",
  "description": "Runs your coding agents for the apps you allow, on your own machine",
  "bin": { "agentd": "daemon.js" },
  "engines": { "node": ">=20" }
}
JSON

# The tray program (tray/, Go): $TRAY_DIR holds a build of every platform (scripts/build-tray.sh); without it one is built
# here when Go is installed. A build without Go and without TRAY_DIR has no tray, and says so.
if [ -z "${TRAY_DIR:-}" ] && command -v go >/dev/null; then
  TRAY_DIR="$STAGE/tray-build"
  "$ROOT/scripts/build-tray.sh" "$TRAY_DIR"
fi
if [ -n "${TRAY_DIR:-}" ]; then
  if [ ! -d "$TRAY_DIR" ]; then
    echo "TRAY_DIR $TRAY_DIR is not a directory" >&2
    exit 1
  fi
  mkdir "$PKG/tray"
  cp -R "$TRAY_DIR"/. "$PKG/tray/"
else
  echo "no tray program in this build: Go is not installed and TRAY_DIR is not set" >&2
fi

# npm's layout: one top-level package/ directory. gzip -n leaves the timestamp out of the header.
tar -cf - -C "$STAGE" package | gzip -n >"$RELEASE_DIR/$TARBALL"

if command -v sha256sum >/dev/null; then SHA="$(sha256sum "$RELEASE_DIR/$TARBALL" | cut -d' ' -f1)"; else SHA="$(shasum -a 256 "$RELEASE_DIR/$TARBALL" | cut -d' ' -f1)"; fi
SIGNATURE="$("${SIGN[@]}" sign "$KEY" "$VERSION" "$SHA")"
printf '{"version":"%s","url":"/download/v%s/%s","sha256":"%s","signature":"%s"}\n' "$VERSION" "$VERSION" "$TARBALL" "$SHA" "$SIGNATURE" >"$RELEASE_DIR/latest.json"
# install.sh: the values are checked above (version, URL) or computed (sha256); none holds a quote, `|`, `&` or a backslash.
sed -e "s|@VERSION@|$VERSION|g" -e "s|@SHA256@|$SHA|g" -e "s|@TARBALL_URL@|$RELEASE_URL/download/v$VERSION/$TARBALL|g" \
  -e "s|@INSTALL_URL@|$RELEASE_URL/latest/download/install.sh|g" "$ROOT/daemon/install.sh.in" >"$RELEASE_DIR/install.sh"
chmod 755 "$RELEASE_DIR/install.sh"
printf '%s\n' "$VERSION" >"$OUT/latest"
echo "built $RELEASE_DIR/$TARBALL ($(wc -c <"$RELEASE_DIR/$TARBALL" | tr -d ' ') bytes)"
