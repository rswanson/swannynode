#!/usr/bin/env bash
# Install pinned client release binaries (arm64) into /data/bin.
# Skips a client whose installed version already matches the pin.
set -euo pipefail
RETH_VERSION="${RETH_VERSION:?}"        # e.g. v2.4.1
LIGHTHOUSE_VERSION="${LIGHTHOUSE_VERSION:?}" # e.g. v8.2.0
MEVBOOST_VERSION="${MEVBOOST_VERSION:?}"     # e.g. 1.12
BIN="${BIN:-/data/bin}"
mkdir -p "$BIN"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

# fetch <url> <dest>: download and verify against the release's .sha256
# sidecar when one is published (reth publishes them; lighthouse ships PGP
# .asc only, mev-boost a combined checksums file — those warn and proceed).
# A present-but-mismatched checksum is always fatal.
fetch() {
  curl -fsSL -o "$2" "$1"
  if curl -fsSL -o "$2.sha256" "$1.sha256" 2>/dev/null; then
    want=$(awk '{print $1}' "$2.sha256")
    got=$(sha256sum "$2" | awk '{print $1}')
    if [ "$want" != "$got" ]; then
      echo "checksum mismatch for $1 (want $want, got $got)" >&2
      exit 1
    fi
    echo "checksum verified for $(basename "$1")"
  else
    echo "WARN: no .sha256 sidecar published for $(basename "$1"); skipping verification" >&2
  fi
}

if ! "$BIN/reth" --version 2>/dev/null | grep -qF "${RETH_VERSION#v}"; then
  fetch "https://github.com/paradigmxyz/reth/releases/download/${RETH_VERSION}/reth-${RETH_VERSION}-aarch64-unknown-linux-gnu.tar.gz" "$tmp/reth.tgz"
  tar xzf "$tmp/reth.tgz" -C "$tmp" reth
  install -m 0755 "$tmp/reth" "$BIN/reth"
fi

if ! "$BIN/lighthouse" --version 2>/dev/null | grep -qF "${LIGHTHOUSE_VERSION#v}"; then
  fetch "https://github.com/sigp/lighthouse/releases/download/${LIGHTHOUSE_VERSION}/lighthouse-${LIGHTHOUSE_VERSION}-aarch64-unknown-linux-gnu.tar.gz" "$tmp/lighthouse.tgz"
  tar xzf "$tmp/lighthouse.tgz" -C "$tmp" lighthouse
  install -m 0755 "$tmp/lighthouse" "$BIN/lighthouse"
fi

if ! "$BIN/mev-boost" --version 2>/dev/null | grep -qF "${MEVBOOST_VERSION}"; then
  fetch "https://github.com/flashbots/mev-boost/releases/download/v${MEVBOOST_VERSION}/mev-boost_${MEVBOOST_VERSION}_linux_arm64.tar.gz" "$tmp/mev-boost.tgz"
  tar xzf "$tmp/mev-boost.tgz" -C "$tmp" mev-boost
  install -m 0755 "$tmp/mev-boost" "$BIN/mev-boost"
fi
echo "client binaries installed: reth ${RETH_VERSION}, lighthouse ${LIGHTHOUSE_VERSION}, mev-boost ${MEVBOOST_VERSION}"
