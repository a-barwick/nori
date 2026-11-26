#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "$0")/../.." && pwd)"
output_dir="${1:-$project_dir/build/connector-macos}"
relay_url="${NORI_CONNECTOR_RELAY_URL:-}"
version="${NORI_CONNECTOR_VERSION:-0.1.0}"
identity="${NORI_MAC_SIGNING_IDENTITY:--}"
case "$relay_url" in
  https://[A-Za-z0-9.-]*) ;;
  http://127.0.0.1:*|http://localhost:*) ;;
  *) printf '%s\n' "Set NORI_CONNECTOR_RELAY_URL to the relay HTTPS URL." >&2; exit 1 ;;
esac
case "$relay_url" in
  *[!A-Za-z0-9.:/_-]*) printf '%s\n' "Invalid relay URL." >&2; exit 1 ;;
esac
case "$version" in
  *[!A-Za-z0-9.+_-]*|'') printf '%s\n' "Invalid connector version." >&2; exit 1 ;;
esac
if [ "$identity" = "-" ]; then
  case "$relay_url" in
    http://127.0.0.1:*|http://localhost:*) ;;
    *) printf '%s\n' "Use a signing identity for a hosted relay. Ad hoc builds are for loopback testing." >&2; exit 1 ;;
  esac
fi

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
app="$output_dir/Nori Connector.app"
mkdir -p "$output_dir"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Library/LaunchAgents"
cp "$project_dir/connector/macos/Info.plist" "$app/Contents/Info.plist"
mkdir -p "$app/Contents/Resources"
cp "$project_dir/LICENSE" "$app/Contents/Resources/LICENSE"
cp "$project_dir/connector/macos/com.nori.connector.agent.plist" "$app/Contents/Library/LaunchAgents/"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $version" "$app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $version" "$app/Contents/Info.plist"
swift_flags=(-D NORI_STANDALONE)
if [ "$identity" = "-" ]; then
  swift_flags+=(-D NORI_ADHOC)
fi
case "$identity" in
  'Developer ID Application:'*) swift_flags+=(-D NORI_RELEASE) ;;
esac
for arch in arm64 amd64; do
  if [ "$arch" = "amd64" ]; then swift_arch=x86_64; else swift_arch=arm64; fi
  (
    cd "$project_dir/core"
    CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build \
      -ldflags "-X nori/internal/homeconnector.DefaultRelayURL=$relay_url -X nori/internal/homeconnector.Version=$version" \
      -o "$scratch/nori-connector-$arch" ./cmd/nori-connector
  )
  xcrun swiftc -target "$swift_arch-apple-macos13.0" "${swift_flags[@]}" \
    -o "$scratch/nori-connector-keychain-$arch" "$project_dir/macos/native/NoriKeychain.swift" -framework Security
  xcrun swiftc -parse-as-library -target "$swift_arch-apple-macos13.0" \
    -o "$scratch/nori-connector-ui-$arch" "$project_dir/connector/macos/NoriConnector.swift" \
    -framework AppKit -framework SwiftUI -framework ServiceManagement
done
lipo -create "$scratch/nori-connector-arm64" "$scratch/nori-connector-amd64" -output "$app/Contents/MacOS/nori-connector"
lipo -create "$scratch/nori-connector-keychain-arm64" "$scratch/nori-connector-keychain-amd64" -output "$app/Contents/MacOS/nori-connector-keychain"
lipo -create "$scratch/nori-connector-ui-arm64" "$scratch/nori-connector-ui-amd64" -output "$app/Contents/MacOS/Nori Connector"
if [ "$identity" = "-" ]; then
  sign_args=(--force --sign -)
else
  sign_args=(--force --options runtime --timestamp --sign "$identity")
fi
codesign "${sign_args[@]}" --identifier com.nori.connector.engine "$app/Contents/MacOS/nori-connector"
codesign "${sign_args[@]}" --identifier com.nori.connector.keychain "$app/Contents/MacOS/nori-connector-keychain"
codesign "${sign_args[@]}" "$app/Contents/MacOS/Nori Connector"
codesign "${sign_args[@]}" "$app"
codesign --verify --deep --strict "$app"
if [ "${NORI_CONNECTOR_DMG:-0}" = "1" ]; then
  dmg="$output_dir/Nori-Connector-$version.dmg"
  rm -f "$dmg"
  mkdir -p "$scratch/dmg"
  ditto "$app" "$scratch/dmg/Nori Connector.app"
  ln -s /Applications "$scratch/dmg/Applications"
  hdiutil create -volname "Nori Connector" -srcfolder "$scratch/dmg" -ov -format UDZO "$dmg"
  if [ "$identity" != "-" ]; then
    codesign --force --timestamp --sign "$identity" "$dmg"
  fi
  if [ -n "${NORI_MAC_NOTARY_PROFILE:-}" ]; then
    if [ "$identity" = "-" ]; then
      printf '%s\n' "Notarization requires a Developer ID signing identity." >&2
      exit 1
    fi
    if [ -n "${NORI_MAC_NOTARY_KEYCHAIN:-}" ]; then
      xcrun notarytool submit "$dmg" --keychain-profile "$NORI_MAC_NOTARY_PROFILE" --keychain "$NORI_MAC_NOTARY_KEYCHAIN" --wait
    else
      xcrun notarytool submit "$dmg" --keychain-profile "$NORI_MAC_NOTARY_PROFILE" --wait
    fi
    xcrun stapler staple "$dmg"
  fi
fi
printf 'Built %s\n' "$app"
