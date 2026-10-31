#!/usr/bin/env bash
# Build release archives of this OpenList fork (with the MiCloud driver).
#
#   scripts/release-micloud.sh v4.2.9-micloud.1 [outdir]
#
# The upstream version is the tag up to "-micloud"; the matching frontend
# release is embedded. Binaries are pure Go (CGO disabled; OpenList uses a
# pure-Go SQLite driver on these targets).
set -euo pipefail

tag="${1:?usage: $0 <tag like v4.2.9-micloud.1> [outdir]}"
out="${2:-build/micloud-release}"
upstream="${tag%%-micloud*}"
web_version="${WEB_VERSION:-$upstream}"
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

cd "$(dirname "$0")/.."

if [[ "$(cat public/dist/VERSION 2>/dev/null)" != "$web_version" ]]; then
  tmp="$(mktemp -d)"
  gh release download "$web_version" -R OpenListTeam/OpenList-Frontend \
    -p "openlist-frontend-dist-$web_version.tar.gz" -D "$tmp"
  find public/dist -mindepth 1 ! -name README.md -exec rm -rf {} +
  tar -xzf "$tmp/openlist-frontend-dist-$web_version.tar.gz" -C public/dist
  rm -rf "$tmp"
fi

pkg=github.com/OpenListTeam/OpenList/v4/internal/conf
ldflags="-w -s \
  -X '$pkg.BuiltAt=$(date -u +'%F %T %z')' \
  -X '$pkg.GitAuthor=$(git log -1 --format=%an)' \
  -X '$pkg.GitCommit=$(git rev-parse --short HEAD)' \
  -X '$pkg.Version=$tag' \
  -X '$pkg.WebVersion=${web_version#v}'"

rm -rf "$out"
mkdir -p "$out"
for target in "${targets[@]}"; do
  os="${target%/*}" arch="${target#*/}"
  name="openlist-micloud-$os-$arch"
  bin="openlist"
  [[ "$os" == windows ]] && bin="openlist.exe"
  stage="$out/$name"
  mkdir -p "$stage"
  echo "building $target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -tags=jsoniter -ldflags="$ldflags" -o "$stage/$bin" .
  cp LICENSE "$stage/"
  if [[ "$os" == windows ]]; then
    (cd "$out" && zip -qr "$name.zip" "$name")
  else
    tar -czf "$out/$name.tar.gz" -C "$out" "$name"
  fi
  rm -rf "$stage"
done
(cd "$out" && sha256sum ./*.tar.gz ./*.zip | sed 's| \./| |' > sha256sums.txt)
ls -l "$out"
