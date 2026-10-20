#!/bin/bash
# Builds the user-machine daemon: the Go engine (cmd/agentd) for darwin and linux on arm64 and amd64, its launcher and the tray
# programs packed as one npm-shaped tarball, plus the index the server serves.
#
#   scripts/build-daemon.sh <outdir>
#
# Writes the files of a GitHub release into <outdir>/download/v<version>/: agentd-<version>.tgz, latest.json ({version, url,
# sha256, signature}) and install.sh (daemon/install.sh.in with this release's values). <outdir>/latest holds the version, which
# cmd/agentd-server (the local stand-in for GitHub Releases) serves as the latest release. The version is
# $VERSION (RELEASE=1: required, x.y.z, and an existing tarball of it is never overwritten), or 0.0.0-g<git describe> (a semver prerelease; the "g" keeps an all-digit hash from reading as a numeric
# identifier with leading zeros, which semver forbids), or 0.0.0-dev when there is no git history. package/ of the tarball holds agentd-<os>-<arch>
# (the engine of each platform; install.sh and the updater install the one of the machine as <home>/daemon/<version>/agentd),
# launcher.sh, package.json and tray/. Run the result: untar it and run package/agentd-<os>-<arch> version
#
# RELEASE_URL is the releases page the daemon takes updates from and install.sh downloads from, baked in:
# https://github.com/<owner>/<repo>/releases (the default of a RELEASE=1 build), or the local stand-in's origin in development.
#
# Every build is signed (cmd/sign-daemon): `signature` is the Ed25519 signature of the version and the tarball's
# sha256, and every engine bakes in the public key set a daemon checks every update against. The private key is the file
# $DAEMON_SIGNING_KEY (PKCS8 PEM). Without it the dev key .dev-signing/daemon-signing.key is used, made on first use:
# daemons built with it accept only updates built with it.
#
# RELEASE=1 requires $DAEMON_SIGNING_KEY. The set it bakes is daemon/release.pub and, when the file exists,
# daemon/release-backup.pub (the backup key, whose private half is kept offline); the signing key must be one of them,
# so a wrong key never ships daemons that installed ones refuse, and a release signed by the backup key replaces a lost or
# leaked primary key. A build that is not RELEASE=1 bakes only its own key and refuses both of the repository's keys, so a
# development build never carries a signature every installed daemon trusts.
# RELEASE_PUB_FILE names one more public key that a build that is not RELEASE=1 refuses (the tests use it, as the
# release private key is not theirs to hold); a RELEASE=1 build ignores it and reads the repository's two files only.
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
  # and published but refused by every daemon, and it is embedded in -ldflags -X.
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
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
(cd "$ROOT" && go build -o "$STAGE/sign-daemon" ./cmd/sign-daemon)
SIGN=("$STAGE/sign-daemon")
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
RELEASE_PUB="$ROOT/daemon/release.pub"
BACKUP_PUB="$ROOT/daemon/release-backup.pub"
if [ ! -f "$RELEASE_PUB" ]; then
  echo "$RELEASE_PUB is missing: it is the public key every daemon checks updates against" >&2
  exit 1
fi
is_public_key() { cmp -s <(printf '%s\n' "$PUB") "$1"; }
if [ "${RELEASE:-}" = 1 ]; then
  # A release bakes in the key set: daemon/release.pub and, when the file exists, daemon/release-backup.pub, and either
  # key may have signed it. A release signed by the backup key is how a lost or leaked primary key is replaced.
  RELEASE_KEY="$("${SIGN[@]}" b64 "$RELEASE_PUB")" || { echo "$RELEASE_PUB is not an Ed25519 public key" >&2; exit 1; }
  SIGNED_BY="daemon/release.pub"
  SIGNER_OK=0
  is_public_key "$RELEASE_PUB" && SIGNER_OK=1
  if [ -e "$BACKUP_PUB" ]; then
    BACKUP_KEY="$("${SIGN[@]}" b64 "$BACKUP_PUB")" || { echo "$BACKUP_PUB is not an Ed25519 public key" >&2; exit 1; }
    if [ "$BACKUP_KEY" = "$RELEASE_KEY" ]; then
      echo "daemon/release-backup.pub is the same key as daemon/release.pub: a backup must be another key" >&2
      exit 1
    fi
    RELEASE_KEY="$RELEASE_KEY,$BACKUP_KEY"
    SIGNED_BY="daemon/release.pub (nor daemon/release-backup.pub)"
    is_public_key "$BACKUP_PUB" && SIGNER_OK=1
  fi
  if [ "$SIGNER_OK" != 1 ]; then
    echo "RELEASE=1: the public key of DAEMON_SIGNING_KEY is not $SIGNED_BY; installed daemons would refuse this build" >&2
    exit 1
  fi
else
  for repo_key in "$RELEASE_PUB" "$BACKUP_PUB" "${RELEASE_PUB_FILE:-}"; do
    if [ -n "$repo_key" ] && [ -e "$repo_key" ] && is_public_key "$repo_key"; then
      echo "the release key signs only RELEASE=1 builds: a development build signed with it would be an update every installed daemon takes, and it points at a development server" >&2
      exit 1
    fi
  done
  RELEASE_KEY="$("${SIGN[@]}" public-b64 "$KEY")"
fi

# The releases page the daemon takes updates from, baked into the engine (AGENTD_RELEASE_URL overrides it at run time).
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

PKG="$STAGE/package"
mkdir "$PKG"

# One static engine per platform: the tarball is the same for every machine, as the update protocol is. No cgo, so every
# platform builds from any machine.
HOME_PKG=github.com/nguyenvanduocit/agentd/daemon/home
LDFLAGS="-s -w -X $HOME_PKG.Version=$VERSION -X $HOME_PKG.ReleaseKey=$RELEASE_KEY -X $HOME_PKG.ReleaseURL=$RELEASE_URL"
for os in darwin linux; do
  for arch in arm64 amd64; do
    (cd "$ROOT" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$PKG/agentd-$os-$arch" ./cmd/agentd)
    chmod 755 "$PKG/agentd-$os-$arch"
  done
done
# The launcher install.sh puts at ~/.agentd/bin/agentd; the daemon refreshes it from here after an update.
cp "$ROOT/daemon/launcher.sh" "$PKG/launcher.sh"
chmod 755 "$PKG/launcher.sh"

cat >"$PKG/package.json" <<JSON
{
  "name": "agentd",
  "version": "$VERSION",
  "description": "Runs your coding agents for the apps you allow, on your own machine"
}
JSON

# The tray program (app/, Go): $TRAY_DIR holds a build of every platform (scripts/build-tray.sh); without it one is built here.
if [ -z "${TRAY_DIR:-}" ]; then
  TRAY_DIR="$STAGE/tray-build"
  "$ROOT/scripts/build-tray.sh" "$TRAY_DIR"
fi
if [ ! -d "$TRAY_DIR" ]; then
  echo "TRAY_DIR $TRAY_DIR is not a directory" >&2
  exit 1
fi
mkdir "$PKG/tray"
cp -R "$TRAY_DIR"/. "$PKG/tray/"

# npm's layout: one top-level package/ directory. gzip -n leaves the timestamp out of the header. COPYFILE_DISABLE keeps
# macOS tar from adding a ._<name> member for each file's extended attributes (they would unpack as files of their own).
COPYFILE_DISABLE=1 tar -cf - -C "$STAGE" package | gzip -n >"$RELEASE_DIR/$TARBALL"

if command -v sha256sum >/dev/null; then SHA="$(sha256sum "$RELEASE_DIR/$TARBALL" | cut -d' ' -f1)"; else SHA="$(shasum -a 256 "$RELEASE_DIR/$TARBALL" | cut -d' ' -f1)"; fi
SIGNATURE="$("${SIGN[@]}" sign "$KEY" "$VERSION" "$SHA")"
printf '{"version":"%s","url":"/download/v%s/%s","sha256":"%s","signature":"%s"}\n' "$VERSION" "$VERSION" "$TARBALL" "$SHA" "$SIGNATURE" >"$RELEASE_DIR/latest.json"
# install.sh: the values are checked above (version, URL) or computed (sha256); none holds a quote, `|`, `&` or a backslash.
sed -e "s|@VERSION@|$VERSION|g" -e "s|@SHA256@|$SHA|g" -e "s|@TARBALL_URL@|$RELEASE_URL/download/v$VERSION/$TARBALL|g" \
  -e "s|@INSTALL_URL@|$RELEASE_URL/latest/download/install.sh|g" "$ROOT/daemon/install.sh.in" >"$RELEASE_DIR/install.sh"
chmod 755 "$RELEASE_DIR/install.sh"
printf '%s\n' "$VERSION" >"$OUT/latest"
echo "built $RELEASE_DIR/$TARBALL ($(wc -c <"$RELEASE_DIR/$TARBALL" | tr -d ' ') bytes)"
