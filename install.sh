#!/bin/sh
# gusset installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/justinstimatze/gusset/main/install.sh | sh
#
# Downloads the latest release archive for your OS/arch from GitHub Releases,
# verifies its SHA-256 against the release's checksums.txt (and, if the GitHub
# CLI is installed, its SLSA build-provenance attestation), and installs the
# `gusset` binary to ~/.local/bin (override with GUSSET_BINDIR).
#
# Pin a version with GUSSET_VERSION=v1.2.3, or pass it as the first argument.
# This script never touches your Firefox profile or config — it only installs
# the binary. Run `gusset doctor` afterwards to see what it finds.
set -eu

REPO="justinstimatze/gusset"
BINDIR="${GUSSET_BINDIR:-$HOME/.local/bin}"
VERSION="${GUSSET_VERSION:-${1:-}}"

say()  { printf '%s\n' "$*"; }
err()  { printf 'install.sh: %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# A downloader: prefer curl, fall back to wget. -fL follows redirects and fails
# on HTTP errors so a 404 doesn't get silently saved as the "binary".
download() { # url outfile
  if have curl; then curl -fsSL "$1" -o "$2"
  elif have wget; then wget -qO "$2" "$1"
  else die "need curl or wget to download"; fi
}
fetch() { # url -> stdout
  if have curl; then curl -fsSL "$1"
  elif have wget; then wget -qO- "$1"
  else die "need curl or wget"; fi
}

detect_os() {
  os=$(uname -s)
  case "$os" in
    Linux)  echo linux ;;
    Darwin) echo darwin ;;
    *) die "unsupported OS '$os' — gusset ships linux and darwin binaries (Windows: use install.ps1). Build from source: https://github.com/$REPO" ;;
  esac
}

detect_arch() {
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64)   echo amd64 ;;
    aarch64|arm64)  echo arm64 ;;
    *) die "unsupported arch '$arch' — gusset ships amd64 and arm64. Build from source: https://github.com/$REPO" ;;
  esac
}

# Resolve the tag to install: explicit override, else the latest published
# release. The /releases/latest API 404s when no release exists yet.
resolve_version() {
  if [ -n "$VERSION" ]; then echo "$VERSION"; return; fi
  tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$tag" ] || die "no published release found yet. Once one is tagged this script will work; until then build from source: git clone https://github.com/$REPO && cd gusset && make build"
  echo "$tag"
}

sha256_of() { # file -> hash
  if have sha256sum; then sha256sum "$1" | cut -d' ' -f1
  elif have shasum; then shasum -a 256 "$1" | cut -d' ' -f1
  else err "no sha256sum/shasum found — skipping checksum verification"; echo ""; fi
}

main() {
  os=$(detect_os); arch=$(detect_arch); tag=$(resolve_version)
  ver=${tag#v} # archive names use the version without the leading 'v'
  archive="gusset_${ver}_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/$tag"

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/gusset-install.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT

  say "→ downloading gusset $tag ($os/$arch)"
  download "$base/$archive" "$tmp/$archive"
  download "$base/checksums.txt" "$tmp/checksums.txt"

  want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1 || true)
  got=$(sha256_of "$tmp/$archive")
  if [ -n "$got" ] && [ -n "$want" ]; then
    [ "$got" = "$want" ] || die "checksum mismatch for $archive (expected $want, got $got) — refusing to install"
    say "✓ sha256 verified"
  else
    err "could not verify checksum (continuing) — checksums.txt entry: '${want:-missing}'"
  fi

  # Stronger than the checksum: verify the SLSA build provenance, if gh is here.
  if have gh; then
    if gh attestation verify "$tmp/$archive" --repo "$REPO" >/dev/null 2>&1; then
      say "✓ build provenance verified (SLSA attestation)"
    else
      err "provenance check skipped or failed (gh attestation verify) — checksum still passed"
    fi
  fi

  tar -xzf "$tmp/$archive" -C "$tmp"
  [ -f "$tmp/gusset" ] || die "archive did not contain a 'gusset' binary"

  mkdir -p "$BINDIR"
  install -m 0755 "$tmp/gusset" "$BINDIR/gusset" 2>/dev/null || {
    cp "$tmp/gusset" "$BINDIR/gusset" && chmod 0755 "$BINDIR/gusset"
  }
  say "✓ installed gusset $tag to $BINDIR/gusset"

  case ":$PATH:" in
    *":$BINDIR:"*) ;;
    *) say ""; say "⚠ $BINDIR is not on your PATH. Add it, e.g.:"
       say "    echo 'export PATH=\"$BINDIR:\$PATH\"' >> ~/.profile && . ~/.profile" ;;
  esac

  say ""
  say "next steps:"
  say "  gusset doctor          # confirm it can find your Firefox profile (read-only)"
  say "  gusset gen-passphrase  # make a passphrase to share across your devices"
  say "  gusset init            # create the config (prints a command to pair other devices)"
  say "See https://github.com/$REPO/blob/main/TESTING.md for the full quickstart."
}

main
