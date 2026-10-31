# Nori docs site

Keep pages visual, concise, and focused on one topic. Use a diagram, flow, or table where helpful, short explanations, and optional depth in Data buttons or `details.more`. Use plain sentences and concrete examples; avoid slogans, repeated captions, prose shorthand, and em dashes. Use the terms defined in `words.html`.

Reuse existing static HTML, `site.css`, and `site.js`. Add pages to `NAV`; let the script generate navigation and links. Use relative links and preserve heading anchors. Reuse existing CSS classes rather than adding a framework or build step.

- Prefer responsive `.flow`/`.node` diagrams; use SVG when position matters. Colors: blue outer loop, green inner loop/Temporal, orange computer, gray runtime/storage, yellow waiting. Label ambiguous arrows and avoid overlaps.
- Code on `main` establishes what is built; Austin's accepted decisions establish what is planned. Mark work outside `main` with `class="designed"` and uncommitted work with `class="in-review"`. Put wholly planned pages in the Planned navigation group. Keep text readable when the filter hides marked content.
- Verify technical facts against code. Link sources at the bottom, use issue identifiers for Linear links, and date costs or measurements. Record material source conflicts in `sources.html`. Never publish secrets, personal email addresses, or real user messages/task IDs.
- Data buttons describe implemented behavior: use real endpoints/field names and source-file references, synthetic values, and `…` for shortened examples. Connect `data-flow` keys to `window.FLOWS` in `task-data.js`, loaded before `site.js`; request steps use `request-data.js`.

For rendered page changes, use `docs/serve.sh` at port 3220 to check affected pages at desktop/phone widths and exercise changed filters or Data buttons. No site run is needed for instruction-only edits.
