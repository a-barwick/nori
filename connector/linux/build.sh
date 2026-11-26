#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "$0")/../.." && pwd)"
output_dir="${1:-$project_dir/build/connector-linux}"
relay_url="${NORI_CONNECTOR_RELAY_URL:-}"
relay_url="${relay_url%/}"
version="${NORI_CONNECTOR_VERSION:-0.1.0}"

# Match core/internal/homeconnector/config.go: origin-only HTTPS URL (or loopback HTTP),
# with no path, query, fragment, or userinfo.
if [[ "$relay_url" =~ ^https://([A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$ ]]; then
  :
elif [[ "$relay_url" =~ ^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$ ]]; then
  :
else
  printf '%s\n' "Set NORI_CONNECTOR_RELAY_URL to an origin-only HTTPS URL (no path, query, or userinfo)." >&2
  exit 1
fi

case "$version" in
  *[!A-Za-z0-9.+_-]*|'') printf '%s\n' "Invalid connector version." >&2; exit 1 ;;
esac

command -v nfpm >/dev/null || { printf '%s\n' "Install nfpm v2.49.0." >&2; exit 1; }
nfpm_version="$(nfpm --version 2>&1 || true)"
if [[ "$nfpm_version" != *"2.49.0"* ]]; then
  printf '%s\n' "Expected nfpm v2.49.0 (got: $nfpm_version)." >&2
  exit 1
fi

mkdir -p "$output_dir"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
built_debs=()
for arch in amd64 arm64; do
  binary="$scratch/nori-connector-$arch"
  (
    cd "$project_dir/core"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
      -ldflags "-X nori/internal/homeconnector.DefaultRelayURL=$relay_url -X nori/internal/homeconnector.Version=$version" \
      -o "$binary" ./cmd/nori-connector
  )
  deb_name="nori-connector-$version-$arch.deb"
  (
    cd "$project_dir"
    NORI_PACKAGE_BINARY="$binary" NORI_PACKAGE_ARCH="$arch" NORI_CONNECTOR_VERSION="$version" \
      nfpm package --config connector/linux/nfpm.yaml --packager deb \
      --target "$output_dir/$deb_name"
  )
  built_debs+=("$deb_name")
done
(
  cd "$output_dir"
  if command -v sha256sum >/dev/null; then
    sha256sum "${built_debs[@]}" > "nori-connector-$version-sha256.txt"
  else
    shasum -a 256 "${built_debs[@]}" > "nori-connector-$version-sha256.txt"
  fi
)
