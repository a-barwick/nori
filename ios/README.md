# Nori for iOS

Native SwiftUI iOS client for Nori (iOS 18+, Swift 6, zero external dependencies).

## Structure

- `ios/Nori.xcodeproj` — Xcode app project targeting iOS 18+ (`com.nori.ios`).
- `ios/NoriApp/` — Thin `@main` SwiftUI app shell, `Info.plist` (`nori://` OAuth & navigation deep links, local networking ATS), and `Assets.xcassets` (AccentColor `#5000FF` + onigiri AppIcon).
- `ios/NoriKit/` — Local Swift Package containing `NoriCore` (models, live + demo transports, SSE, `NoriStore`), `NoriDesign` (adaptive Light/Dark tokens, `NoriAppearance`, animated `NoriMark`, reusable primitives), and `NoriFeatures` (Chat, Work, Apps, Library, More, Memory, Automations, Goals, Settings).
- **Design System:** See [`docs/design-system.md`](../docs/design-system.md) (base tokens & principles) and [`ios/DESIGN_SYSTEM.md`](DESIGN_SYSTEM.md) (iOS SwiftUI APIs, Dark Mode, Liquid Glass, and simulator preview workflow).

## 1. Open and Run in Xcode

1. Open the project in Xcode:
   ```bash
   open ios/Nori.xcodeproj
   ```
2. Select the **Nori** scheme and pick an iOS Simulator (e.g. **iPhone 18 Pro**) or your connected iPhone.
   - To run on a physical iPhone, select the **Nori** target → **Signing & Capabilities**, choose your Apple Developer Team, and change the bundle identifier if `com.nori.ios` is already taken on your team.
3. Press **⌘R** to build and run.

Command-line build and unit tests:
```bash
# Run NoriCore unit tests on macOS
swift test --package-path ios/NoriKit

# Build the iOS app for Simulator
xcodebuild \
  -project ios/Nori.xcodeproj \
  -scheme Nori \
  -destination 'platform=iOS Simulator,name=iPhone 18 Pro' \
  build
```

## 2. Immediate Exploration in Demo Mode

On first launch, Nori starts in **Demo Mode** — an in-memory simulation pre-seeded with a realistic workspace (Q4 Vendor Renewal Brief, procurement approval card, Friday design review calendar question, MCP servers including Linear sign-in, 3-layer Memory with 24 KB budget, Automations, Goals, and Permissions).

No running backend is required to test messaging, approvals, task steering, document reading/revision, memory pinning/promotion, or MCP configuration.

## 3. Connecting to Live `nori-local`

> **Production reminder:** Never pause or stop production Nori services for local iOS testing. Always run against the local stack or a sandbox.

### Start the local Go backend on your Mac

From the repository root:
```bash
make deps         # Starts local Postgres via Docker Compose
make local-core   # Runs cmd/nori-local on http://127.0.0.1:8080
```
Ensure you have provisioned a local user matching your email (`core/cmd/nori-user`) and configured `.env`.

### Option A: iOS Simulator (`127.0.0.1:8080`)

The iOS Simulator shares the Mac's loopback network directly:
1. Open **More → Server** (or **Settings**) in the Nori iOS app.
2. Select the **Simulator `nori-local`** profile (`http://127.0.0.1:8080`) and enter your local user email (matching `NORI_LOCAL_EMAIL`).
3. Tap **Test Connection** to verify `/healthz`, then save/switch to connect.

### Option B: Physical iPhone over Mac LAN or Tailscale

`cmd/nori-local` binds to `127.0.0.1:8080` by default (loopback-only). To reach it from a physical iPhone on the same Wi-Fi or Tailscale tailnet:

- **Tailscale (recommended — provides automatic HTTPS):**
  ```bash
  tailscale serve --bg 8080
  ```
  Then enter `https://<your-mac>.<tailnet>.ts.net` in **More → Server** (no ATS exceptions needed).
- **Local Wi-Fi / LAN:**
  Expose port `8080` on your Mac's LAN interface (for example, `BIND_HOST=0.0.0.0` when running a server that supports it, or forwarding `0.0.0.0:8080 -> 127.0.0.1:8080` over your local network), and enter `http://<your-mac>.local:8080` (or `http://<mac-lan-ip>:8080`) in **More → Server**.
  - `Info.plist` includes `NSAllowsLocalNetworking = true` and `NSLocalNetworkUsageDescription`, so iOS prompts once for Local Network access and permits plain HTTP only for local/LAN hosts while keeping HTTPS enforced for public hosts.

## 4. Connecting to a Hosted BFF / OIDC Deployment

1. Open **More → Server** (or **Settings**).
2. Choose **Hosted BFF / OIDC** and enter your deployment's Nori BFF base URL (for example, `https://host.example.com/api/nori`).
3. Sign in via the OAuth / OIDC flow (`nori://` is registered in `CFBundleURLTypes` for OAuth callback redirects). Tokens are stored in the iOS Keychain, and requests authenticate via Bearer token against the hosted `/api/nori` endpoints over HTTPS.

## 5. Sentry Cocoa Observability & dSYM Uploads

Nori for iOS ships with `sentry-cocoa` wired in `NoriApp` (`SentryNoriTelemetry.swift`), `os.Logger` + `OSLogStore` diagnostics in `NoriCore`, and W3C `traceparent` + `sentry-trace` + `X-Nori-Client: ios` propagation into Go Core.

### Local and TestFlight Configuration
Build settings live in `ios/Config/Base.xcconfig`, `Debug.xcconfig`, and `Release.xcconfig` (and are exposed in `Info.plist`). To enable Sentry locally without committing a DSN, create gitignored `ios/Config/Local.xcconfig`:
```xcconfig
// Note: in .xcconfig, `//` starts a comment, so write `https:/$()/`
SENTRY_DSN = https:/$()/<public-key>@<host>/<project-id>
SENTRY_ENVIRONMENT = development
SENTRY_TRACES_SAMPLE_RATE = 1.0
```

For TestFlight or App Store archives, pass `SENTRY_DSN="$SENTRY_IOS_DSN"` as an Xcode build setting in CI. An empty DSN disables Sentry. For a Debug build with Sentry enabled, open **More → Diagnostics → Send Test Event** and look for `diagnostics.test` in the configured iOS project. Submitting a chat turn produces an iOS transaction whose trace ID can also be searched in Go Core's Sentry project when both projects have tracing enabled.

### Uploading dSYMs for TestFlight / App Store Releases
The **iOS Archive** GitHub Actions workflow builds a Release archive and checks that its dSYM UUID matches the app. The shared Xcode scheme can upload symbols from the archive you actually distribute. To enable that upload for local archives, add these settings to `ios/Config/Local.xcconfig`:

```xcconfig
NORI_UPLOAD_IOS_DSYMS = YES
SENTRY_ORG = your-org
SENTRY_PROJECT_IOS = nori-ios
```

Install `sentry-cli` and configure its organization token with `sentry-cli login` before choosing **Product → Archive**. Xcode records post-action output in the archive build log.

In a TestFlight CI job, install `sentry-cli` (`brew install --cask sentry-cli`), then call the repository action with the same archive you export for TestFlight. The action verifies the UUIDs and waits for Sentry to process the upload.

```yaml
- name: Upload TestFlight symbols to Sentry
  uses: ./.github/actions/ios-symbols
  with:
    archive-path: ${{ runner.temp }}/Nori.xcarchive
  env:
    SENTRY_ORG: ${{ vars.SENTRY_ORG }}
    SENTRY_PROJECT_IOS: ${{ vars.SENTRY_PROJECT_IOS }}
    SENTRY_AUTH_TOKEN: ${{ secrets.SENTRY_AUTH_TOKEN }}
```

For a manual retry, run `./scripts/upload-ios-dsyms.sh /path/to/Nori.xcarchive` with the same Sentry settings.

### In-App Diagnostics
Open **More → Diagnostics** to inspect the current build, Sentry status, resolved User ID, and last Trace ID, or tap **Export Diagnostic Logs** to share a redacted report of recent `OSLogStore` entries. Every task detail view also includes a tap-to-copy **Task ID** at the bottom for jumping straight to its trace in Sentry.
