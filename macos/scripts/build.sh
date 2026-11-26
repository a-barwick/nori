#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "$0")/../.." && pwd)"
mac_dir="$project_dir/macos"
output_dir="${1:-$project_dir/.artifacts/nori-mac}"
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"

: "${NORI_MAC_WEB_ORIGIN:?Set NORI_MAC_WEB_ORIGIN}"
: "${NORI_CONNECTOR_RELAY_URL:?Set NORI_CONNECTOR_RELAY_URL}"
identity="${NORI_MAC_SIGNING_IDENTITY:--}"
builder_identity="$identity"
if [[ "$builder_identity" == "Developer ID Application: "* ]]; then
  builder_identity="${builder_identity#Developer ID Application: }"
fi
version="${NORI_MAC_VERSION:-$(node -p "require('$mac_dir/package.json').version")}"

if [[ "${NORI_MAC_RELEASE:-0}" == "1" ]]; then
  if [[ "$identity" != "Developer ID Application:"* || -z "${NORI_MAC_NOTARY_PROFILE:-}" ]]; then
    echo "A release requires a Developer ID Application identity and a notarization profile." >&2
    exit 1
  fi
fi
if [[ -n "${NORI_MAC_NOTARY_PROFILE:-}" && "$identity" != "Developer ID Application:"* ]]; then
  echo "Notarization requires a Developer ID Application identity." >&2
  exit 1
fi
case "$version" in
  *[!A-Za-z0-9.+_-]*|'') echo "Invalid version." >&2; exit 1 ;;
esac

cd "$mac_dir"
node <<'NODE'
const fs = require("node:fs");

function origin(name) {
  const value = process.env[name];
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`${name} must be an HTTPS origin.`);
  }
  const local = process.env.NORI_MAC_ALLOW_LOOPBACK === "1" &&
    url.protocol === "http:" && url.hostname === "127.0.0.1";
  if ((url.protocol !== "https:" && !local) || url.username || url.password ||
      url.pathname !== "/" || url.search || url.hash || value.endsWith("?") || value.endsWith("#")) {
    throw new Error(`${name} must be an HTTPS origin or an explicitly allowed numeric loopback origin.`);
  }
  return { value: url.origin, local };
}
const web = origin("NORI_MAC_WEB_ORIGIN");
const relay = origin("NORI_CONNECTOR_RELAY_URL");
process.env.NORI_MAC_BROWSER_ORIGIN ||= web.value;
const browser = origin("NORI_MAC_BROWSER_ORIGIN");
if (web.local !== relay.local) throw new Error("Web and relay origins must both target the same environment.");
if ((web.local || browser.local) && process.env.NORI_MAC_RELEASE === "1") throw new Error("A release cannot use loopback origins.");
if (!web.local && (!process.env.NORI_MAC_SIGNING_IDENTITY || process.env.NORI_MAC_SIGNING_IDENTITY === "-")) {
  throw new Error("Production origins require a signed app; ad hoc builds are for loopback testing.");
}
const googleClientId = process.env.NORI_MAC_GOOGLE_CLIENT_ID || "";
const googleClientSecret = process.env.NORI_MAC_GOOGLE_CLIENT_SECRET || "";
if (!web.local && (!googleClientId || !googleClientSecret)) throw new Error("Configure the Mac Google OAuth client ID and secret.");
fs.mkdirSync(".build/native", { recursive: true });
fs.writeFileSync("config.json", JSON.stringify({
  webOrigin: web.value,
  browserOrigin: browser.value,
  relayOrigin: relay.value,
  googleClientId,
  googleClientSecret,
}) + "\n", { mode: 0o600 });
NODE

relay_origin="$(node -p "require('./config.json').relayOrigin")"
(
  cd "$project_dir/core"
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
    -ldflags "-X nori/internal/homeconnector.DefaultRelayURL=$relay_origin -X nori/internal/homeconnector.Version=$version -X nori/internal/homeconnector.DesktopMode=1" \
    -o "$mac_dir/.build/native/nori-connector" ./cmd/nori-connector
)
swift_flags=(-D NORI_DESKTOP)
if [[ "$identity" == "-" ]]; then
  swift_flags+=(-D NORI_ADHOC)
fi
if [[ "$identity" == "Developer ID Application:"* ]]; then
  swift_flags+=(-D NORI_RELEASE)
fi
xcrun swiftc -target arm64-apple-macos13.0 "${swift_flags[@]}" \
  -o .build/native/nori-connector-keychain native/NoriKeychain.swift -framework Security
xcrun swiftc -target arm64-apple-macos13.0 \
  -o .build/native/nori-desktop-service native/NoriDesktopService.swift -framework ServiceManagement -framework Foundation

iconset="$mac_dir/.build/Nori.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$project_dir/ios/NoriApp/Assets.xcassets/AppIcon.appiconset/AppIcon.png" \
    --out "$iconset/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$project_dir/ios/NoriApp/Assets.xcassets/AppIcon.appiconset/AppIcon.png" \
    --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o .build/Nori.icns

builder_type=distribution
if [[ "$identity" == "Apple Development:"* || "$identity" == "Mac Developer:"* ]]; then
  builder_type=development
fi
./node_modules/.bin/electron-builder --mac dmg --arm64 --publish never \
  --config electron-builder.yml \
  --config.directories.output="$output_dir" \
  --config.extraMetadata.version="$version" \
  --config.mac.identity="$builder_identity" --config.mac.type="$builder_type"

app="$output_dir/mac-arm64/Nori.app"
dmg="$output_dir/Nori-$version-arm64.dmg"
codesign --verify --deep --strict "$app"
if [[ -n "${NORI_MAC_NOTARY_PROFILE:-}" ]]; then
  if [[ -n "${NORI_MAC_NOTARY_KEYCHAIN:-}" ]]; then
    xcrun notarytool submit "$dmg" --keychain-profile "$NORI_MAC_NOTARY_PROFILE" --keychain "$NORI_MAC_NOTARY_KEYCHAIN" --wait
  else
    xcrun notarytool submit "$dmg" --keychain-profile "$NORI_MAC_NOTARY_PROFILE" --wait
  fi
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
fi
printf 'Built %s\n' "$dmg"
