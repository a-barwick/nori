# Nori for Mac

Nori for Mac presents the shared Nori Web interface and includes the Home AI connector. It supports Apple silicon and macOS 13 or later.

Install by opening the DMG and dragging Nori to Applications. Sign in and open **More → Models** to enable **Use this Mac as a Home AI Server**. The setup checks Ollama and its installed models, asks for background access, and offers Chat and Work model choices once. Those choices can be changed later in Models.

The helper runs as the signed-in macOS user when background access is enabled. It runs while the main window is closed and reconnects after the Mac wakes or its network returns. Ollama stays on numeric loopback. The relay connection is outbound. The Mac must be awake and Ollama must be running to serve inference.

Turning sharing off, signing out, or switching accounts stops the helper and revokes the Mac's device pairing. If the relay is unreachable, Nori retains a revocation-only credential in Keychain and retries when Nori is open again. Pairing another account waits until the previous disconnect completes. The standalone connector remains available for headless servers.

## Build

Install the dependencies with `npm ci --prefix macos`. The build runs on an Apple silicon Mac with Xcode command-line tools, Go, and Node.js.

A local loopback build is useful for testing. It uses ad hoc signing and only accepts explicitly allowed numeric loopback origins.

```sh
NORI_MAC_ALLOW_LOOPBACK=1 \
NORI_MAC_WEB_ORIGIN=http://127.0.0.1:13210 \
NORI_CONNECTOR_RELAY_URL=http://127.0.0.1:18092 \
macos/scripts/build.sh /tmp/nori-mac-build
```

The script writes `mac-arm64/Nori.app` and `Nori-<version>-arm64.dmg` in its output directory. The default output is `.artifacts/nori-mac`. It compiles the Go connector with the relay origin embedded and ignores connector environment overrides in the packaged app. It also generates `macos/config.json`, which is included in the signed app. Treat the desktop OAuth client as a public client. The embedded OAuth client secret is visible in the application bundle and must be configured accordingly.

A signed build targeting hosted Nori needs these values.

```sh
export NORI_MAC_WEB_ORIGIN=https://<desktop-web-origin>
export NORI_MAC_BROWSER_ORIGIN=https://noribot.chat
export NORI_CONNECTOR_RELAY_URL=https://<home-ai-relay-origin>
export NORI_MAC_GOOGLE_CLIENT_ID=<desktop-oauth-client-id>
export NORI_MAC_GOOGLE_CLIENT_SECRET=<desktop-oauth-client-secret>
export NORI_MAC_SIGNING_IDENTITY="Developer ID Application: <name> (<team-id>)"
```

`NORI_MAC_BROWSER_ORIGIN` defaults to the Web origin. It identifies the browser-facing Nori origin for connected-app handoffs. `NORI_MAC_VERSION` optionally overrides the package version. An Apple Development identity can be used for a hosted development build on an authorized Mac.

## Signing and notarization

Install a **Developer ID Application** certificate and private key in the signing keychain. Configure a notarytool keychain profile for the same Apple developer team, and expose the signing keychain to `codesign`. Set `NORI_MAC_NOTARY_KEYCHAIN` if the notarytool profile is in a nondefault keychain.

```sh
xcrun notarytool store-credentials "nori-notary" \
  --apple-id "<apple-id>" --team-id "<team-id>" --password "<app-specific-password>"
export NORI_MAC_NOTARY_PROFILE=nori-notary
export NORI_MAC_RELEASE=1
macos/scripts/build.sh /tmp/nori-mac-release
```

The release build requires the Developer ID identity, the notarytool profile, and hosted HTTPS origins. It signs the nested native executables and Electron bundle with hardened runtime, signs the DMG, submits the DMG to Apple, and staples and validates its ticket. Check the resulting bundle and disk image before distribution.

```sh
codesign --verify --deep --strict --verbose=2 /tmp/nori-mac-release/mac-arm64/Nori.app
xcrun stapler validate /tmp/nori-mac-release/Nori-<version>-arm64.dmg
spctl --assess --type execute --verbose=4 /tmp/nori-mac-release/mac-arm64/Nori.app
```

The desktop OAuth client must be accepted by the authentication configuration for the desktop Web origin. The relay, Core schema, and Home AI settings must be deployed and configured before the app can pair against that environment. See [Home AI Server](../core/infrastructure/home-ai.md) for the server rollout.
