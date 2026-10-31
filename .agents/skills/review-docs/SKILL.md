---
name: review-docs
description: Review a Nori docs site page in docs/site for style, structure and factual accuracy. Use when asked to review, audit or check docs, or after writing a page with write-docs.
---

# Review docs

Review as someone who didn't write the page. Assume it has at least one problem until you've checked each item below.

## 1. Run the checks

```bash
python3 .agents/skills/review-docs/scripts/check.py docs/site/page.html
```

`FAIL` lines must be fixed. For `WARN` lines, fix the text or be able to say why it's fine. The script can't judge meaning, so a clean run doesn't finish the review.

## 2. Read for the reader

Read `docs/site/AGENTS.md` and `.agents/skills/plain-writing/SKILL.md`, then answer these questions:

- Can a reader get the idea from the opening visual and the lede alone?
- Does the visual show the mechanism, with system parts in the boxes and what passes on the arrows?
- Does the page cover one topic? Do two sections answer the same question?
- What would a reader do with each number, name and setting? Cut those with no answer.
- Does each table row describe one thing?
- Does the page describe the system, with no instructions for contributors or agents?
- Are details that most readers don't need behind `details.more`?
- Does the page repeat another page where a link would do?
- Does each sentence name what happened, in the glossary's words?

## 3. Check the facts

Pick at least five specific claims, and prefer names, numbers and statements about order. Open the code for each one. Also check that:

- The listed sources exist and support the page.
- Anything not on `main` is marked `designed` and reads properly when hidden.
- Examples are made up and include no secrets, personal data or real task IDs.

## 4. Look at it

Serve the site with `docs/serve.sh`, or `docs/serve.sh 3221` if the port is taken, or open the file directly. View the page at 1440 px and 375 px wide.

- The page doesn't scroll sideways, and nothing is clipped or overlapping.
- Diagram lines don't cross boxes or text, no box is stretched empty, and colors follow the rules in `docs/site/AGENTS.md`.
- The console shows no errors, and the filter and Data buttons work if present.

If you can't render it, say that this part wasn't done.

## 5. Report

List findings by severity, each with the file, the exact text or element and the fix. Separate wrong facts from style. If nothing is wrong, say which claims you checked. Fix findings only if you were asked to.
