# Nori for iOS

Native SwiftUI iOS client for Nori (iOS 18+, Swift 6, zero external dependencies).

## Structure

- `ios/Nori.xcodeproj` — Xcode app project targeting iOS 18+ (`com.nori.ios`).
- `ios/NoriApp/` — Thin `@main` SwiftUI app shell, `Info.plist` (`nori://` OAuth & navigation deep links, local networking ATS), and `Assets.xcassets` (AccentColor `#5000FF` + onigiri AppIcon).
- `ios/NoriKit/` — Local Swift Package containing `NoriCore` (models, live + demo transports, SSE, `NoriStore`), `NoriDesign` (Nori tokens, animated `NoriMark`, reusable primitives), and `NoriFeatures` (Chat, Work, Apps, Library, More, Memory, Automations, Goals, Settings).

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
