#!/bin/sh
# Builds release binaries with the dashboard embedded, plus SHA256SUMS.
# Usage: scripts/release.sh VERSION
set -eu
cd "$(dirname "$0")/.."
version=${1:?usage: scripts/release.sh VERSION}
(cd web && npm ci && npm run build)
rm -rf release && mkdir release
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=
  [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$version" \
    -o "release/nebula-$version-$os-$arch$ext" ./cmd/nebula
done
(cd release && sha256sum nebula-* > SHA256SUMS)
ls -l release
