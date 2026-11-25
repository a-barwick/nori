# Nori Design System — Base Foundations

Platform-agnostic visual identity, semantic token architecture, and interaction rules for Nori across all clients (iOS, Web, and embedded surfaces).

> **Platform extensions:** For SwiftUI tokens, native Liquid Glass chrome, sheet contracts, and simulator verification, see [`ios/DESIGN_SYSTEM.md`](../ios/DESIGN_SYSTEM.md). For Web CSS/Tailwind v4 tokens, anti-FOUC appearance wiring, and layout contracts, see [`web/DESIGN_SYSTEM.md`](../web/DESIGN_SYSTEM.md).

---

## 1. Core Principles

**Purple communicates interaction, neutral surfaces communicate structure, semantic colors communicate state, and the mascot communicates personality.**

1. **Same personality across appearances.** Light and Dark modes share the same calm, approachable character—never two disconnected themes.
2. **True-dark negative space in Dark Mode.** Anchor dark canvases at true OLED black (`#000000`) with whisper-quiet obsidian surfaces (`#0B0B0F`) so the canvas breathes and content never drowns in charcoal slabs.
3. **One separation mechanism at a time (Luminance *or* Border).**
   - In **Light Mode**, white cards on a white canvas rely on `1px` hairlines (`#E7E2EF`).
   - In **Dark Mode**, subtle luminance contrast (`#0B0B0F` on `#000000`) does the separation—cards, inner callouts, inputs, and disabled controls should be **borderless** (or at most `6%` white hairlines where flat structural dividers are required). Never double-outline a filled dark card with a bright stroke.
4. **Flatten nested boxes.** Avoid boxes-inside-boxes. Inner notices, metadata pills, and disabled buttons inside a card should rely on quiet tinted fills rather than competing outlines.
5. **Purple is earned.** Reserve saturated brand purple for primary actions, user messages, and live activity indicators; reserve lighter accent purple for links, selected icons, pills, and focus rings. Never use permanent bright purple outlines on unfocused chrome.
6. **Calm async ergonomics.** Work and action histories should read like an activity console, not a second chat thread. Reserve warning amber for approvals/uncertainty and red strictly for failures and destructive actions.

---

## 2. Token Architecture

Nori follows a **semantic-first token hierarchy** (aligned with W3C Design Tokens / Apple HIG / Linear conventions). Feature code must reference **semantic roles**, never raw hex values or ad-hoc appearance conditionals.

```text
Primitive Values (sRGB hex / alpha)
   └─▶ Semantic Tokens (background.*, text.*, border.*, brand.*, status.*, mascot.*)
         └─▶ Component Tokens & Primitives (buttons, cards, pills, sheets, composer)
```

---

## 3. Color Tokens

### 3.1 Foundation Surfaces & Elevation Hierarchy

Depth in Dark Mode comes from quiet luminance steps, **not** drop shadows.

| Token | Role | Light | Dark |
| --- | --- | --- | --- |
| `background.canvas` | Level 0 — Primary app canvas, conversation stream | `#FFFFFF` | `#000000` |
| `background.surface` | Level 1 — Cards, grouped rows, work/memory containers | `#FFFFFF` | `#0B0B0F` |
| `background.sheet` | Level 1 (modal) — Bottom sheets and modal editors | `#FFFFFF` | `#0E0E13` |
| `background.recessed` | Resolved / inactive cards, quiet secondary panels | `#F8F6FC` | `#07070A` |
| `background.tinted` | Subtle violet-tinted highlight (running task, budget bar, session gist) | `#FAF8FE` | `#100E18` |
| `background.elevated` | Level 2 — Composer, text inputs, neutral chips, toasts | `#F7F5FB` | `#14141A` |
| `background.selected` | Level 3 — Selected tabs, active category chips, tonal fills | `#EEE8FA` | `#261F38` |

### 3.2 Borders & Overlays

| Token | Role | Light | Dark |
| --- | --- | --- | --- |
| `border.card` | Card & input outer boundary (hairline in light, borderless in dark) | `#E7E2EF` | Transparent (`0%`) |
| `border.subtle` | Internal list dividers and subtle horizontal rules | `#E7E2EF` | `#FFFFFF` @ `6%` |
| `border.strong` | Interactive outlines (outlined secondary buttons, checkboxes) | `#CFC3E9` | `#FFFFFF` @ `14%` |
| `border.accent` | Subtle purple accent rim (active selection / tonal highlights) | `#DFD4F6` | `#A98AFF` @ `20%` |
| `overlay.scrim` | Modal backdrop scrim | `#160531` @ `26%` | `#000000` @ `68%` |

### 3.3 Typography

| Token | Role | Light | Dark |
| --- | --- | --- | --- |
| `text.primary` | Headings, conversation body, row titles | `#190936` | `#F2EFF8` |
| `text.secondary` | Descriptions, subtitles, supporting copy | `#726D82` | `#A9A2B8` |
| `text.muted` | Timestamps, placeholders, quiet metadata | `#6E6880` | `#878196` |
| `text.disabled` | Unavailable controls, disabled labels | `#A29CB0` | `#6B6578` |
| `text.onBrand` | Foreground text/icons on `brand.primary` fills | `#FFFFFF` | `#FFFFFF` |

### 3.4 Brand & Accent Purple (WCAG Contrast Split)

Never collapse `brand.primary` and `brand.accent` into a single dark-mode token:

- **`brand.primary` (`#7A3DF6` dark)** is saturated for **filled surfaces carrying white text** (primary buttons, user chat bubbles, active toggles, activity indicators). White on `#7A3DF6` maintains WCAG AA contrast (`~5.5:1`).
- **`brand.accent` (`#A98AFF` dark)** is lifted for **text, icons, links, selected tab labels, pills, and focus rings on dark surfaces**. `#A98AFF` on `#000000` / `#0B0B0F` exceeds WCAG AA (`>6.5:1`).

| Token | Role | Light | Dark |
| --- | --- | --- | --- |
| `brand.primary` | Primary CTA buttons, user message bubbles, activity dot | `#5000FF` | `#7A3DF6` |
| `brand.primaryPressed` | Pressed / hover state for primary buttons | `#4200D6` | `#692FE0` |
| `brand.accent` | Links, text actions, selected icons, pills, focus rings | `#5000FF` | `#A98AFF` |
| `brand.subtle` | Tonal purple backgrounds (working state, icon wells) | `#F1EBFF` | `#1B162C` |

### 3.5 Semantic Status Colors

Async workflows rely on six consistent foreground/background state pairs across Chat, Work, Actions, Routines, Notifications, and Approval cards. In Dark Mode, status pills and notices are **fill-only (borderless)**.

| State | Usage | Light FG / BG | Dark FG / BG |
| --- | --- | --- | --- |
| **Success** | Completed, Confirmed, Ready, Connected | `#1C7A43` / `#E9F8EF` | `#85D4A4` / `#0F2019` |
| **Working** | In progress, Working, Connecting, Active | `#5000FF` / `#F1EBFF` | `#A98AFF` / `#1B162C` |
| **Warning** | Attention required, Needs approval, Waiting, Checking | `#946000` / `#FFF4DC` | `#F2C276` / `#21190F` |
| **Error** | Failure, Failed, Offline, Destructive action | `#CC1F33` / `#FFF0F2` | `#FF9BA7` / `#25131B` |
| **Scheduled** | Scheduled routines, upcoming automations | `#1D5BB8` / `#EAF2FE` | `#99B9EF` / `#111A28` |
| **Inactive** | Cancelled, Paused, Stopped, Expired, Neutral | `#6E6880` / `#F7F5FB` | `#878196` / `#14141A` |

---

## 4. Mascot (`NoriMark`)

The onigiri mascot uses dedicated appearance-aware tokens and must **never** be auto-inverted by system filters. The expression and seaweed shape stay identical across modes; no glow, drop shadow, or gradient is applied.

| Token | Role | Light | Dark |
| --- | --- | --- | --- |
| `mascot.body` | Rice silhouette fill | `#FDFCFF` | `#C6C4CE` |
| `mascot.seaweed` | Bottom nori wrap (clipped to body) | `#101A30` | `#171522` |
| `mascot.ink` | Eyes and smile | `#190936` | `#171522` |
| `mascot.edge` | Outer silhouette stroke (`1.75pt` light / subtle `1.35pt` dark @ 96px) | `#190936` | `#D1CFD8` |
| `mascot.activity` | Live attention / activity indicator dot | `#5000FF` | `#7A3DF6` |

Motion states: `idle` (periodic blink), `attentive` (slight downward glance toward composer), `thinking` (2s smooth eye sweep). Always disable eye animations when Reduce Motion is enabled.

---

## 5. Typography, Spacing & Geometry

### 5.1 Type Scale

Use platform-native system sans-serif (**SF Pro** on iOS, **Figtree** on Web) and system/IBM Plex monospace for tool IDs, budgets, and technical diagnostics. Respect OS Dynamic Type / user font scaling in both appearances.

| Role | Size | Weight | Notes |
| --- | --- | --- | --- |
| Display / Hero | `30–32 pt` | Bold | Sign-in and major empty states |
| Screen / Nav Title | `22–26 pt` | Bold | Primary screen headers |
| Card Heading | `17 pt` | Semibold | Approval & clarification titles |
| Section Heading | `16 pt` | Semibold | Subsections and empty titles |
| Conversation / Body | `15–17 pt` | Regular / Medium | Assistant prose & user bubbles |
| Row Title | `14.5 pt` | Semibold | List items and action audit rows |
| Metadata / Supporting | `13 pt` | Regular / Semibold | Timestamps, labels, section caps |
| Caption / Pill | `11.5–12 pt` | Medium / Semibold | Status pills, counters, footnotes |
| Monospace | `11.5–12.5 pt` | Medium | Tool IDs, budgets, endpoints |

### 5.2 Spacing Grid (4 pt Base)

Use multiples of `4 pt`: `4`, `8`, `12`, `16`, `20`, `24`, `32`.

### 5.3 Corner Radii

| Component | Radius |
| --- | --- |
| Chips & icon wells | `10 pt` |
| Input fields & attachment previews | `12 pt` |
| List rows / compact tiles | `14 pt` |
| Content cards & grouped row containers | `16 pt` |
| User message bubbles & approval/clarification cards | `20 pt` |
| Modal sheets | `28 pt` |
| Buttons, composer, status pills, floating navigation | Capsule (`999 pt`) |

---

## 6. Component Characteristics

- **Conversation:** Assistant messages sit directly on `background.canvas` (no bubble container). User messages use `brand.primary` fill + `text.onBrand` text. Inline links use `brand.accent`.
- **Composer & Attachments:** Sits on `background.elevated` (or native glass material). Unfocused state is borderless in Dark Mode; `brand.accent` focus ring (`1.5 pt`) appears only while focused. Attachment chips use `12 pt` corners; upload failures use the Error status fill/foreground pair.
- **Approval vs. Clarification Cards:**
  - Both share `background.surface` and `20 pt` corners, borderless in Dark Mode.
  - **Approval cards** signal urgency through a warm subdued amber warning callout (`status.warningBg` fill, no border) and an amber expiry pill—**not** a loud amber outer frame. Primary `Approve` remains `brand.primary`; `Decline` is an outlined secondary button.
  - **Clarification cards** omit warning colors entirely so questions feel calmer than authorization gates.
- **Buttons:** Support 5 semantic kinds—`Primary` (`brand.primary`), `Secondary` (surface + `border.strong`), `Tonal` (`background.selected` + `brand.accent`), `Quiet` (`background.elevated`), and `Destructive` (`status.errorBg` + `status.error`)—plus a visibly muted **Disabled** state (`#14141A` bg, `#6B6578` fg, no border in dark mode) handled centrally by the button component.
- **Modal Sheets:** Sit one surface level above the canvas (`background.sheet` `#0E0E13`), with inputs slightly lighter (`background.elevated` `#14141A`) and selected categories using soft violet surfaces (`background.selected` `#261F38`) rather than saturated fills.
- **Copy & Sublabels:**
  - **No filler sublabels.** Only show a row subtitle or card sublabel when it displays real live data or state (e.g., `0.3 / 24 KB`, `2 connected`, `3 rules`, `Dark`, `America/Los_Angeles · $5.00 / task`). Never add explanatory filler that repeats a title, describes an obvious control, or fills an empty state with extra taglines.
  - **Plain, positive English.** When a screen needs a brief explanation (such as empty Goals), write one clear, positive sentence for non-technical readers—never define something by what it is *not*.

---

## 7. Guardrails for Contributors & Agents

- **Never hardcode hex colors** in feature views or components—add or reuse a semantic token.
- **Never render white text on `text.primary` (`ink`).** In Dark Mode `text.primary` is near-white (`#F2EFF8`), causing invisible white-on-white controls (toasts, selected chips, segmented tabs). Always use dedicated semantic tokens (`toastBg`/`toastFg`, `chipSelectedBg`/`chipSelectedFg`).
- **Never lock the app to a single color scheme.** Default to `System` and support `System`, `Light`, and `Dark` with immediate propagation to presented sheets, keyboards, mascot, and system chrome.
- **Never stack dark borders on top of lifted dark fills** unless the element is an interactive outlined control or actively focused.
