---
name: write-docs
description: Add or change a page on the Nori docs site in docs/site, in the site's visual-first style. Use when asked to write, add, update, split or rework Nori documentation, including design docs, decisions and architecture pages.
---

# Write docs

This skill is required for every change to a page, however small. Load it before your first edit, and follow these steps in order. Paths are from the repo root.

## 1. Read the rules

Read these files in full before writing:

- `docs/site/AGENTS.md`, the page rules
- `.agents/skills/plain-writing/SKILL.md`, the sentence rules
- `docs/site/words.html`, the glossary
- `docs/site/site.css`, for the available classes
- `docs/site/index.html`, the page every page has to match

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
- Select the facts a reader needs. For each number, name or setting, ask what a reader would do with it, and cut it if there's no answer. Test files, queue sizes, flush timeouts and fallback orders usually go.

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

Where the topic allows, follow one concrete example through the visual. Each box names a system part and each arrow says what passes, as `docs/site/AGENTS.md` describes.

Outline the page as `h1`, one sentence of lede, the visual, then two to four short sections that each answer a different question. Aim for about 300 visible words, as `index.html` has. The limit is 500.

## 5. Write the page

Copy the skeleton in `docs/site/AGENTS.md`. Use its existing classes, and don't add navigation, previous and next links, copy icons or filters, because `site.js` adds them.

- Lead each section with a table, list or diagram, and use prose only for what those can't show.
- Use a concrete example the reader can follow, such as the Tucson weather and dinner tasks.
- Put long lists of tokens, limits and fields in one `details.more` section, and only if a reader would look them up.
- End with `details.sources`, listing repo file paths and linking Linear issues.

When you change an existing page, rework the affected sections so the page reads as one piece. Don't stack new text on top of old text, and remove anything the change makes untrue. Keep existing headings where possible, because link IDs come from them.

## 6. Update what the change touches

- `words.html` for a term the page depends on and the reader may not know, usually one or two at most
- `decisions.html` for a new design decision, with its date and what was rejected
- `open-questions.html` for anything unresolved, and remove questions the change settles
- `code.html` when the page adds a new area of code
- Links from related pages

Keep each of these edits small, and don't touch unrelated pages. Every page you do change must pass completely, so rework any older content that fails.

If a page you're changing makes a claim the code and sources don't support, remove it from that page. Add it to `open-questions.html` if it might still be true, and say so in your report.

## 7. Check it

Run the checks on each page you changed, and fix every failure, including failures that were there before your edit:

```bash
python3 .agents/skills/review-docs/scripts/check.py docs/site/your-page.html
```

View each page at 1440 px and 375 px wide. If you can't render it, the page isn't finished, so say so.

## 8. Get it reviewed

You can't approve your own page. Start a new thread or subagent that didn't write the change, and have it run `.agents/skills/review-docs/SKILL.md` on each changed page. Fix what it rejects, then have the whole page reviewed again until it returns PASS.

## 9. Commit

After the PASS, run `python3 docs/agent_map.py build` and `python3 docs/agent_map.py mark docs/site/your-page.html`. List every code file a page depends on in its Sources, because the staleness check only watches those files.

End the commit message with these lines, and only if both are true:

```text
Docs-Skills: write-docs, plain-writing
Docs-Review: PASS, review-docs in a separate thread
```

The commit hook rejects a commit without them. Don't use `--no-verify`.

## 10. Report

State the pages changed by repo path, the code files you checked, the review verdict and whether you viewed the rendered page. Don't link to a temporary local server.
