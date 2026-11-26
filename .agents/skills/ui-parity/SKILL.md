---
name: ui-parity
description: >-
  Design, preview, and align Nori's UI across Web and iOS: create interactive
  HTML mockups for review, enforce Nori's calm design system, launch the iOS
  Simulator directly in the requested appearance/build, and produce a Web-to-iOS
  parity checklist. Use for UI redesigns, mockups, Dark Mode checks, or
  cross-platform parity passes.
---

# UI Mockups & Web/iOS Parity

Keep Nori's Web and iOS clients visually calm, minimal, and aligned.

## Steps

1. **Load design foundations**
   - Read `docs/design-system.md` and `ios/DESIGN_SYSTEM.md` before designing or editing UI.
   - Enforce Nori's copy and chrome rules:
     - 3-tab structure (`Chat`, `Work`, `More`).
     - No filler sublabels, no redundant tab/page headers, no duplicate status badges, no negative definitions ("what it's not"), and never use the phrase `"needs you"`.
     - True-black dark mode (`#000000`) with restrained, borderless layered cards.

2. **Prototype with interactive HTML mockups first (for redesigns)**
   - Before writing large UI refactors, build a clickable static HTML mockup in the conversation artifact directory so the user can review layout, hierarchy, and copy.
   - Ask clarifying questions if requirements are ambiguous before finalizing the mockup.

3. **Preview on iOS Simulator directly**
   - When the user asks to see an iOS change, build and launch the iOS Simulator yourself—explicitly setting the requested appearance (e.g. `xcrun simctl ui booted appearance dark` for Dark Mode) and booting directly into the updated Nori app.

4. **Cross-platform parity checklist**
   - After shipping UI improvements on Web or iOS, output a concise parity summary listing every behavioral, layout, and copy change that needs to be mirrored to the other client.
