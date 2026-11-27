# Nori Web Design System

Web-specific CSS token architecture, Tailwind v4 `@theme inline` API, Dark Mode & anti-FOUC appearance wiring, layout contracts, and verification workflow for the Next.js web client (`web/`).

> **Base specification:** Read [`docs/design-system.md`](../docs/design-system.md) first for platform-agnostic color theory, dark-mode separation principles, mascot rules, and semantic status meanings. For iOS SwiftUI conventions, see [`ios/DESIGN_SYSTEM.md`](../ios/DESIGN_SYSTEM.md).

---

## 1. Architecture & Source of Truth

All Web visual tokens and reusable primitives live in `web/src/app/globals.css`, `web/src/lib/appearance.ts`, and `web/src/components/`.

| File | Responsibility |
| --- | --- |
| `web/src/app/globals.css` | Light & Dark CSS custom properties (`:root`, `:root[data-appearance="dark"]`, `@media (prefers-color-scheme: dark)`), Tailwind v4 `@theme inline` bindings, typography scale, radii, motion, and shell classes |
| `web/src/lib/appearance.ts` | `NoriAppearance` (`system`, `light`, `dark`), `localStorage` key (`"nori.appearance"`), `useAppearance()` hook, and inline `APPEARANCE_BOOT_SCRIPT` |
| `web/src/app/layout.tsx` | Loads Figtree & IBM Plex Mono fonts and injects `APPEARANCE_BOOT_SCRIPT` in `<head>` to prevent FOUC |
| `web/src/components/mark/NoriMark.tsx` | Appearance-aware SVG mascot (`NoriMark`, `Medallion`) and monochrome navigation icon (`NoriTabIcon`) |
| `web/src/components/ui/primitives.tsx` | Base controls (`Button` with 5 semantic kinds + centralized disabled state, `IconButton` + `Tip`) |
| `web/src/components/assistant/ui.tsx` | Screen & list primitives (`Screen`, `Section`, `Rows`, `Row`, `Pill`, `Badge`, `Notice`, `Empty`, `Confirm`, `UnderlineTabs`, `Tabs`, `Field`, `inputBase`) |
| `web/src/components/shell/AppFrame.tsx` | Full-bleed desktop top bar (`60px`) and mobile bottom 3-tab shell (`Chat`, `Work`, `More`) |

---

## 2. CSS & Tailwind v4 Token API

Feature code must reference Tailwind semantic utilities (backed by `@theme inline`) or semantic CSS variables—never hardcode hex values or `rgba(...)` strings inside components.

### Surfaces & Borders
```text
bg-canvas          --bg-canvas         (#FFFFFF / #000000)   Level 0 OLED canvas
bg-surface         --bg-surface        (#FFFFFF / #0B0B0F)   Level 1 cards & grouped rows
bg-sheet           --bg-sheet          (#FFFFFF / #0E0E13)   Modal dialogs & slide-over sheets
bg-sidebar         --bg-sidebar        (#F8F6FC / #07070A)   Resolved / recessed cards
bg-wash-top        --bg-wash-top       (#FAF8FE / #100E18)   Tinted highlight & running task cards
bg-fill-1          --fill-1            (#F7F5FB / #14141A)   Level 2 inputs, neutral chips, elevated wells
bg-fill-2          --fill-2            (#EEE8FA / #261F38)   Level 3 selected fills & tonal backgrounds

border-card-border --border-card       (#E7E2EF / transparent) Card & input edges (borderless in Dark Mode)
border-hairline    --hairline          (#E7E2EF / white @ 6%)  Internal dividers & structural rules
border-hairline-strong --hairline-strong (#CFC3E9 / white @ 14%) Outlined buttons & interactive rims
border-lavender-border --lavender-border (#DFD4F6 / #A98AFF @ 20%) Accent rims on active highlights
```

### Typography & Brand (WCAG Contrast Split)
```text
text-ink           --text-primary      (#190936 / #F2EFF8)
text-ink-2         --text-secondary    (#726D82 / #A9A2B8)
text-ink-3         --text-tertiary     (#6E6880 / #878196)
text-disabled      --text-disabled     (#A29CB0 / #6B6578)
bg-disabled-bg     --disabled-bg       (#ECE9F4 / #14141A)
text-on-accent     --text-on-accent    (#FFFFFF / #FFFFFF)

bg-accent          --accent            (#5000FF / #7A3DF6)   Filled CTAs, user bubbles, active dot
bg-accent-hover    --accent-hover      (#4200D6 / #692FE0)   Primary button hover
text-accent-text   --accent-text       (#5000FF / #A98AFF)   Links, active nav labels, text actions, underline tabs
border-accent-signal --accent-signal   (#7943FF / #A98AFF)   Focused input ring
bg-accent-subtle   --accent-subtle     (#F1EBFF / #1B162C)   Tonal purple icon wells & highlights
```

### Semantic Tones (`Pill` & `Notice`)
Use `Tone` (`"ok" | "working" | "warning" | "danger" | "scheduled" | "neutral" | "accent"`). Status badges and notices are fill-only (borderless) in Dark Mode:

- `ok` — Completed, Confirmed, Ready, Live (`#1C7A43`/`#E9F8EF` light, `#85D4A4`/`#0F2019` dark)
- `working` — In progress, Working, Active (`#5000FF`/`#F1EBFF` light, `#A98AFF`/`#1B162C` dark)
- `warning` — Action needed, Needs approval, Checking (`#946000`/`#FFF4DC` light, `#F2C276`/`#21190F` dark)
- `danger` — Failed, Offline, Destructive (`#CC1F33`/`#FFF0F2` light, `#FF9BA7`/`#25131B` dark)
- `scheduled` — Scheduled automations & routines (`#1D5BB8`/`#EAF2FE` light, `#99B9EF`/`#111A28` dark)
- `neutral` — Cancelled, Paused, Stopped, Inactive (`#6E6880`/`#F7F5FB` light, `#878196`/`#14141A` dark)

### Selected Chips & Tooltips (No `bg-ink text-surface`)
Never pair `bg-ink` with `text-surface` for selected pills, tabs, or tooltips (in Dark Mode `ink` is near-white). Always use:
- Selected filter chips / toggles: `border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg`
- Tooltips & dark overlays: `bg-toast-bg text-toast-fg`

---

## 3. Appearance Switching & Anti-FOUC Boot

- **Storage key:** `nori.appearance` in `localStorage` (matching iOS `NoriAppearance.storageKey`), defaulting to `"system"`.
- **DOM contract:**
  - `"system"` removes `data-appearance` from `<html>` so `@media (prefers-color-scheme: dark)` controls Dark Mode natively.
  - `"light"` sets `data-appearance="light"` on `<html>`.
  - `"dark"` sets `data-appearance="dark"` on `<html>`.
- **Zero-flash boot:** `web/src/app/layout.tsx` runs `APPEARANCE_BOOT_SCRIPT` synchronously in `<head>` (with `suppressHydrationWarning` on `<html>`) so Dark Mode resolves before first paint.
- **Settings UI:** Exposed at **More → Settings → Appearance** (`/settings/appearance`), featuring a live interactive chat preview and a `System | Light | Dark` segmented radio group.

---

## 4. Web Shell, Layout & Component Contracts

### Unified Column & Top Navigation
- Every primary screen (`Chat`, `Work`, `More`, and detail pages) aligns to the unified `960px` centered column (`.nori-page-col`, `32px` desktop padding, `18px` mobile padding).
- Desktop renders a calm `60px` top bar with the Nori mark on the left and a centered 3-pillar pill switcher (`Chat`, `Work`, `More`). Mobile (`< 640px`) renders a bottom 3-tab bar.
- The `Chat` tab uses `NoriTabIcon` (solid negative-space onigiri when active, outlined silhouette when inactive), matching iOS `NoriMark.tabBarIcon(selected:)`.
- Top-level tab hubs (`Work`, `More`) omit redundant visible page titles via `<Screen hideHeaderTitle>` while keeping an `<h1 className="sr-only">` for keyboard/screen-reader focus management.

### Single-Column Full-Width Hub Stacks
- **Work → Artifacts | Automations | Tasks:** Three underline tabs in order (`Artifacts`, `Automations`, `Tasks`, defaulting to `Tasks`).
- **Work → Tasks:** `Action needed` approval cards above a single unified list of open tasks (active tasks with circular progress rings and inline-expandable read-only steps, queued tasks, and one-off scheduled tasks), with inline `Cancel` / `Keep` confirmation and no archived task history. Shows a slim single-row `All clear` bar when idle.
- **Work → Automations:** Recurring routines, monitors, and event-relative automations with quick Run / Pause controls.
- **Work → Artifacts:** Starts directly with Search + Kind filter chips + clean chronological list (no oversized hero banner pulling the newest item out of flow).
- **More:** Flat grouped rows under **Workspace** (`Apps & MCP`, `Memory`), **Settings** (`Models`, `Standing Permissions`, `Preferences & Budgets`, `Appearance`), and **Account** (`Account`, `Action History`, `Diagnostics`)—no top hero card or oversized 2-up workspace tiles.

### Chat Chrome & Mascot
- Assistant replies sit directly on `bg-canvas` (`--bg-bubble-nori: transparent`); user replies use `bg-accent text-on-accent`.
- Only a **single active `NoriMark`** appears in the thread: beside the latest Nori reply when idle, or beside the thinking `WorkLine` while `conversationBusy` is true. Older Nori messages preserve a `w-7 mr-3` alignment spacer so prose remains vertically aligned.
- `NoriMark` reads separate `--mark-body`, `--mark-seaweed`, `--mark-ink`, `--mark-edge`, and `--mark-edge-width` variables so Dark Mode uses `#C6C4CE` rice with a subtle `#D1CFD8` (`1.35`) edge and `#171522` eyes/seaweed—never CSS `filter: invert(...)`.
- Respect `@media (prefers-reduced-motion: reduce)` for all animations (blinks, eye sweeps, slide-over sheets, arrival transitions).

### Buttons & Inputs
- Use `<Button variant="primary | neutral | tonal | quiet | danger" size="sm | md">`. Disabled styling (`bg-disabled-bg text-disabled shadow-none`) is handled centrally in `primitives.tsx`—do not add `disabled:opacity-50` hacks at call sites.
- Inputs (`inputBase` / `inputClass`) sit on `bg-fill-1` with `border-card-border` (hairline in Light, borderless in Dark) and transition to `border-accent-signal` on `:focus-visible`.

### Copy & Sublabels
- **No filler sublabels.** Only show a row `detail` or card subtitle when it carries live data or state (e.g., `0.3 / 24 KB`, `3 rules`, `Dark`, `2 connected`). Never add explanatory filler that repeats the title, explains an obvious control, or fills an empty state with extra taglines.
- **Plain, positive English.** When a screen needs a brief explanation, write one clear, positive sentence for non-technical readers—never start by defining what something is *not*.

---

## 5. Verification & Local QA Workflow

1. **Typegen, Typecheck, Lint & Unit Tests:**
   ```bash
   cd web && ./node_modules/.bin/next typegen && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src && bun test
   ```
2. **Production Build Check:**
   ```bash
   cd web && bun run build
   ```
3. **Local Live Demo (Light & Dark):**
   ```bash
   make local-web   # or `make dev` to run web + local core together on http://127.0.0.1:3210
   ```
   Verify every primary screen (`/`, `/work`, `/work?tab=artifacts`, `/work?tab=automations`, `/more`, `/settings/appearance`) in both **Light** and **Dark** modes at desktop (`1280px+`) and mobile (`390px`) widths.
