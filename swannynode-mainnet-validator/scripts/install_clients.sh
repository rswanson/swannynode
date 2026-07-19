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

if ! "$BIN/reth" --version 2>/dev/null | grep -qF "${RETH_VERSION#v}"; then
  curl -fsSL -o "$tmp/reth.tgz" \
    "https://github.com/paradigmxyz/reth/releases/download/${RETH_VERSION}/reth-${RETH_VERSION}-aarch64-unknown-linux-gnu.tar.gz"
  tar xzf "$tmp/reth.tgz" -C "$tmp" reth
  install -m 0755 "$tmp/reth" "$BIN/reth"
fi

if ! "$BIN/lighthouse" --version 2>/dev/null | grep -qF "${LIGHTHOUSE_VERSION#v}"; then
  curl -fsSL -o "$tmp/lighthouse.tgz" \
    "https://github.com/sigp/lighthouse/releases/download/${LIGHTHOUSE_VERSION}/lighthouse-${LIGHTHOUSE_VERSION}-aarch64-unknown-linux-gnu.tar.gz"
  tar xzf "$tmp/lighthouse.tgz" -C "$tmp" lighthouse
  install -m 0755 "$tmp/lighthouse" "$BIN/lighthouse"
fi

if ! "$BIN/mev-boost" --version 2>/dev/null | grep -qF "${MEVBOOST_VERSION}"; then
  curl -fsSL -o "$tmp/mev-boost.tgz" \
    "https://github.com/flashbots/mev-boost/releases/download/v${MEVBOOST_VERSION}/mev-boost_${MEVBOOST_VERSION}_linux_arm64.tar.gz"
  tar xzf "$tmp/mev-boost.tgz" -C "$tmp" mev-boost
  install -m 0755 "$tmp/mev-boost" "$BIN/mev-boost"
fi
echo "client binaries installed: reth ${RETH_VERSION}, lighthouse ${LIGHTHOUSE_VERSION}, mev-boost ${MEVBOOST_VERSION}"
