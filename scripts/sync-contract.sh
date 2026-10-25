#!/bin/sh
# Re-copies the upload contract from the Caparaó app repository into testdata/contract, so the
# exporter is tested against exactly the files the app and the stub use.
# Usage: scripts/sync-contract.sh [path-to-caparao-repo]   (default: ../caparao)
set -eu
cd "$(dirname "$0")/.."
src=${1:-../caparao}/contract
[ -f "$src/brew.schema.json" ] || { echo "no contract at $src" >&2; exit 1; }
rm -rf testdata/contract
mkdir -p testdata
cp -R "$src" testdata/contract
echo "copied $src -> testdata/contract; run: go test ./..."
