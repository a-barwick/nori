---
name: review-docs
description: Reject or pass a Nori docs site page in docs/site against the site's strict rules for style, structure and factual accuracy. Required for every page change before commit. Use when asked to review, audit or check docs, or after writing a page with write-docs.
---

# Review docs

Your job is to reject pages that don't match `docs/site/index.html`. You must not have written the change. Assume the page fails until you have evidence for every step.

The verdict is PASS or REJECT. One failed rule rejects the page. There are no minor findings, and you can't waive a rule, because only the repo owner can. A rule without evidence counts as failed.

Read `docs/site/AGENTS.md`, `.agents/skills/plain-writing/SKILL.md` and `docs/site/index.html` first. Rule numbers below come from "The standard" in `docs/site/AGENTS.md`.

## 1. Run the script (rules 1 to 10)

```bash
python3 .agents/skills/review-docs/scripts/check.py docs/site/page.html
```

Any `FAIL` rejects the page. A clean run only means you can continue.

## 2. Test the picture (rules 11 and 12)

Look at the rendered page, and read only the title, lede and first visual. Write one sentence saying how the system works. If you can't, reject it.

List each box and unclear arrow in the visual. Reject a box that isn't a system part, and an arrow that doesn't say what passes.

## 3. Read it as a newcomer (rules 13, 14 and 15)

Read as a good engineer who has never seen Nori.

- Quote each term you'd need another page to understand.
- Find the concrete example. A concept page without one is rejected.
- For each number, name and setting in visible text, say what a reader would do with it. Quote each one with no answer.
- Check the sentences against `plain-writing`. Quote each sentence that names no action or uses a word the glossary replaces.
- Reject a page that has two sections answering the same question, or repeats another page where a link would do.

## 4. Check every claim (rules 16 and 17)

List each number, name, order and status claim in visible text. For each one, open the source and record its `file:line` on `main`, or its Linear issue if it's planned. A short sample isn't enough. A claim you can't trace rejects the page.

- Hide `designed` content with the filter and confirm the page still reads properly.
- Confirm examples are made up, with no secrets, personal data, real task IDs, GitHub links or git history.

## 5. Look at it (rule 18)

Serve the site with `docs/serve.sh`, or `docs/serve.sh 3221` if the port is taken, or open the file directly. Take screenshots at 1440 px and 375 px wide, and view `index.html` at 1440 px beside the page.

- At 1440 px, the title, lede and top of the visual are on the first screen.
- At 375 px, nothing scrolls sideways, clips or overlaps.
- Diagram lines don't cross boxes or text, no box is stretched empty, and colors follow the rules.
- The console shows no errors, and the filter and Data buttons work if present.

If you can't render the page, the verdict is REJECT.

## 6. Report

Start with `PASS` or `REJECT`, then give the evidence:

| Step | Evidence |
| --- | --- |
| Script | The result line |
| Picture | Your one-sentence explanation |
| Newcomer | Each quoted term and fact, or "none" |
| Claims | Each claim with its `file:line` or Linear issue |
| Rendering | The widths you viewed and what you saw |

For a rejection, list each failure with its rule number, the exact text or element and the fix. A PASS without this evidence doesn't count. Fix findings only if you were asked to, and review the whole page again after any fix.
