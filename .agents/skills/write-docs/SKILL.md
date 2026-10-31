---
name: write-docs
description: Add or change a page on the Nori docs site in docs/site, in the site's visual-first style. Use when asked to write, add, update, split or rework Nori documentation, including design docs, decisions and architecture pages.
---

# Write docs

Follow these steps in order, and do each one even if the request sounds small. Paths are from the repo root.

## 1. Read the rules

Read these files in full before writing:

- `docs/site/AGENTS.md`, the page rules
- `.agents/skills/plain-writing/SKILL.md`, the sentence rules
- `docs/site/words.html`, the glossary
- `docs/site/site.css`, for the available classes
- Two model pages: `docs/site/tasks.html` for a flow and `docs/site/web-client.html` for tables

## 2. Find the place

List `docs/site/` and read `NAV` in `docs/site/site.js`. Search the site for the topic.

- If a page covers the topic, change that page and link to it from anywhere else that mentions the topic.
- Create a page only for a topic that has no home. Add it to the matching `NAV` group, and to the Planned group if none of it is built.
- A page covers one topic. If the topic won't fit in about 800 words, plan two pages.

## 3. Get the facts from the code

Read the code on the current branch before you write a claim. The code decides what's built, and Linear and `decisions.html` decide what's planned.

- Keep a list of the files each fact came from, and use it for the Sources section.
- Don't guess a name, limit, endpoint, field or default. If you can't confirm one, leave it out or add it to `open-questions.html`.
- Mark anything that isn't on `main` as `designed`. Never describe planned work as running.
- Select the facts a reader needs. A page with every constant from the code is too long.

## 4. Choose the visual, then outline

The visual comes before any section text. Pick the one that shows the idea:

| The topic is | Use |
| --- | --- |
| A sequence of steps or a request path | `.flow` with `.node` and `.arrow` |
| A choice | `.flow` with `.node.decide` and `.branch` |
| States of something | `.states` |
| Parts and how they connect | `.flow`, or inline SVG if position carries meaning |
| A set of options or fields | A compact table in a `.card` |
| A screen | A screenshot from the running app, with made-up data |

Outline the page as `h1`, one sentence of lede, the visual, then two to five short sections. Aim for 300 to 600 words.

## 5. Write the page

Copy the skeleton in `docs/site/AGENTS.md`. Use its existing classes, and don't add navigation, previous and next links, copy icons or filters, because `site.js` adds them.

- Lead each section with a table, list or diagram, and use prose only for what those can't show.
- Use a concrete example the reader can follow, such as the Tucson weather and dinner tasks.
- Put long lists of tokens, limits and fields in a `details.more` section.
- End with `details.sources`, linking repo files and Linear issues.

When you change an existing page, rework the affected sections so the page reads as one piece. Don't stack new text on top of old text, and remove anything the change makes untrue. Keep existing headings where possible, because link IDs come from them.

## 6. Update what the change touches

- `words.html` for each new term, before using it
- `decisions.html` for a new design decision, with its date and what was rejected
- `open-questions.html` for anything unresolved, and remove questions the change settles
- `code.html` when the page adds a new area of code
- Links from related pages

Keep each of these edits small, and don't touch unrelated pages.

## 7. Check it

Run the review checks on each page you changed, and fix every failure:

```bash
python3 .agents/skills/review-docs/scripts/check.py docs/site/your-page.html
```

Then follow `.agents/skills/review-docs/SKILL.md` on your own page. Serve the site with `docs/serve.sh` and look at the page at desktop and phone widths if you have a browser or a screenshot tool.

## 8. Report

State the pages changed, the code files you checked, what you couldn't verify, and whether you viewed the rendered page. If you couldn't render it, say so.
