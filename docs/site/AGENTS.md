# Writing Nori docs pages

This folder is the Nori docs site. Read this file before adding or changing a page. Only the repo owner can waive a rule.

Every change in this folder, however small, must use the `write-docs`, `review-docs` and `plain-writing` skills in `.agents/skills/`. Load them before the first edit, and finish the `review-docs` steps before merging or publishing.

## What a page should feel like

A reader should get the idea from the picture, understand it from a few short lines, and only open details if they want them. Write the way you'd explain it to a friend who is a good engineer.

1. **Visual first.** Open with a diagram, a state flow or a compact table. If a page has no visual, ask what it should be before writing more text.
2. **Easy to scan.** Use short lists and tables. Keep paragraphs to one or two sentences.
3. **Details stay hidden until asked for.** Put depth in a Data button or a `details.more` section.
4. **Minimal UI.** Add a control or label only if the page needs it.

## The standard

`index.html` is the standard, and every page has to meet it. A page that breaks any rule below is rejected, including a page you only touched for a small fix.

Checked by `.agents/skills/review-docs/scripts/check.py`:

| # | Rule | Limit |
| --- | --- | --- |
| 1 | The lede says what this is, with no code | 20 words |
| 2 | The visual comes straight after the lede | 30 words before it |
| 3 | Visible text, outside `details` | 500 words |
| 4 | All text on the page. Over this, split by topic | 800 words |
| 5 | A sentence, and a paragraph or list item | 30 words, 3 sentences |
| 6 | Code names in visible paragraphs and lists. The rest go in tables, Data buttons or `details.more` | 3 per page |
| 7 | A heading, in plain words with no code | 6 words |
| 8 | A diagram box | 15 words |
| 9 | Implementation terms such as reconcile, bounded, heartbeat, fenced and admit, outside `details` | 0 |
| 10 | Emphasis, stock phrases, em dashes, semicolons and "X, not Y", outside quoted user messages | 0 |

`code.html`, `decisions.html`, `open-questions.html`, `reading.html`, `scenarios.html`, `sources.html` and `words.html` are reference pages, exempt from rules 2, 3, 4, 6 and 9. Only `scenarios.html` is exempt from rules 5 and 10, because its text is test data.

Checked by the reviewer, who has to show the evidence in `review-docs`:

| # | Rule | How to test it |
| --- | --- | --- |
| 11 | The picture teaches the idea | From the visual and lede alone, state how the system works in one sentence. |
| 12 | The visual shows the mechanism | Each box is a system part, each unclear arrow says what passes, and a concept such as "Filters" isn't a box. |
| 13 | A newcomer can follow it | A good engineer who has never seen Nori needs no other page to understand the visible text. Words defined in `words.html`, such as outer loop and turn, are shared vocabulary, so use them and don't reword them. |
| 14 | One concrete example | A concept page follows a real-looking message or task, such as the Tucson weather task. |
| 15 | Every fact earns its place | For each number, name and setting, say what a reader would do with it. Cut it if there's no answer. |
| 16 | Every claim is true | Each number, name, order and status in visible text has a `file:line` in the code on `main`, or a Linear issue the reviewer can read if it's planned. |
| 17 | Built and planned are clear | Planned work is marked `designed`, and the page reads properly with it hidden. |
| 18 | It looks like the first page | At 1440 px the title, lede and top of the visual are on the first screen. At 375 px nothing scrolls sideways or clips. |

## Writing rules

- Describe software as software: processes run, models return output, code saves records. Don't write that Nori does "real work", notices, knows, thinks, understands or decides what matters.
- Don't use praise or emphasis words such as real, actual, honest, true, trust, natural, feel, useful or meaningful unless they state a checkable fact.
- Use full, plain sentences with concrete nouns and verbs. "The runtime saves the message, then schedules a wake."
- Don't write slogans, aphorisms or punchy fragments. Avoid "X, not Y", "It isn't X, it's Y", "the real question", "load-bearing" and similar lines.
- In sentences, don't use `=`, `→`, `+` or `≠` as shorthand, and avoid em dashes. Arrows are fine in diagrams and labels.
- Use real examples, such as the Tucson weather and dinner tasks, instead of abstract descriptions.
- Keep labels short. Don't add subtitles, captions or helper text that only repeat what's on screen.
- Don't narrate the page ("this section explains") or mention how it was written.
- Read each line as a skeptical reader would and cut anything vague or odd-sounding.

Use the same words everywhere: outer loop, inner loop, runtime, task, dispatch, steer, command, event. `words.html` defines them. Don't add synonyms, and add a new term there before using it.

## Page structure

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Page title · Nori</title>
<link rel="stylesheet" href="site.css">
<script src="site.js" defer></script>
</head>
<body>
<h1>Page title</h1>
<p class="lede">One sentence on what this is, in plain words.</p>
<!-- A diagram, state flow or table goes here. -->
<section><h2>A short heading</h2>
<!-- Short lists, tables and cards. -->
</section>
<details class="sources"><summary>Sources</summary><p>Repo file paths and links to other pages.</p></details>
</body>
</html>
```

Then add the page to `NAV` in `site.js`. The script adds the navigation, previous and next links, section lists, copy links and the filter. Don't copy those into a page.

- Keep each page to one topic. Link to another page instead of repeating it.
- Each client gets one `<Platform> client` page in the Clients group. Rules that apply to every client go on `clients.html` or `api.html`.
- Split a page that passes about 800 words into pages by topic. Reference pages such as Scenarios and Decisions can run longer.
- Don't list every value just because the code has it. Leave out what a reader can't act on, such as queue sizes, flush timeouts and fallback chains. Put long lists of tokens, limits or fields in one `details.more` section.
- Give each question one section. Two tables that both answer "where does this go" should be one table.
- In a table, each row describes one thing and each column is a property of it. Don't line up unrelated items in the same row. The first column renders bold, so keep it to a short name.
- Describe the system. Setup steps and instructions for contributors or agents belong in `AGENTS.md` or a README.
- Every heading gets a copy-link icon automatically. Only the icon is interactive, so don't make headings or subtitles clickable. Link IDs come from the heading text, so renaming a heading breaks old links to it.
- Use relative links such as `tasks.html`, because the site won't always be hosted at the root of a domain.
- Use what's already in `site.css`: `card`, `grid2`, `grid3`, `flow`, `node`, `branch`, `states`, `say`, `tag` and `pts`. Add new CSS only when a page needs something those can't do.
- Don't add a framework, a build step or external scripts without asking first.

## Diagrams

- Prefer `.flow` and `.node` HTML diagrams because they adjust to the screen width. Use inline SVG when position carries meaning.
- Keep the colors consistent. Blue is the outer loop, green is the inner loop and Temporal, orange is the computer, gray is the runtime and storage, and yellow is waiting.
- Label every arrow whose meaning isn't obvious, and don't let lines cross boxes or text.
- Keep box text to one short sentence, and move detail to a table or list below the diagram. Name a system part in each box, and say on each arrow what passes between them. A box such as "Filters" or "One request, one trace" is a concept, so it belongs in text.
- Color boxes with `.outer`, `.inner` and `.computer` whenever those parts appear.
- Keep `.branch` columns about the same depth, because a shorter column stretches into an empty box.
- Start each `.branch` box after a `.decide` node with the answer it follows, such as "Yes" or "No".

## Built and planned

Readers must always be able to tell what runs today.

- Mark anything not on `main` with `class="designed"`. Mark uncommitted work with `class="in-review"`. The filter finds both.
- Add `<span class="tag design">designed</span>` where the marking alone isn't clear.
- A page that is entirely planned goes in the Planned group in `NAV`, which labels it automatically.
- If a marked sentence is hidden, the text around it must still read properly.

## Facts

When sources disagree, the code on `main` decides what's built. Linear and `decisions.html` decide what's planned.

- Check limits, names, endpoints and tools against the code before writing them down. Don't copy them from an older doc. Keep units as the code has them: bytes and characters differ.
- Refer to models by role, such as the outer model, unless the page is about model choice. Selected models change.
- If a source is out of date, follow the code and add the conflict to `sources.html`.
- Date any cost or measurement, and say that it's a planning figure if it is.
- Never include secrets, API keys, personal email addresses, or real user messages and task IDs. The site is public.
- The repo is private. Cite repo files as plain paths in `<code>`, and don't link GitHub or mention branches, commits or pull requests.
- Don't use the name Nova, the internal product around the web client. Say "the theme" or "the shell".
- The site is public and the repo and Linear are private. Never name or link Linear or its issue IDs, a person, an account or a cloud project. List repo file paths under Sources, and `docs/agent_map.py publish` removes them, `sources.html` and `code.html` from the published site.
- Record each new design decision in `decisions.html`, with its date and what was rejected. Add unresolved questions to `open-questions.html`, and remove them once settled.

## Data buttons

A Data button shows the request, response and saved record behind a step or a table row.

- Add `data-flow="key"` to a table row, add the key to `window.FLOWS` in `task-data.js`, and load that script before `site.js`. Request page steps read `request-data.js`.
- Take endpoints and field names from the code, and set `source` to the file they came from.
- Values are invented, so use made-up examples. Mark anything shortened with `…`.
- Don't add a Data button to something that isn't built yet, because there's no code to base it on.

## Before you finish

Run the site from the repo root. It opens at http://127.0.0.1:3220. Pass another port if that one is taken, as it often is in a worktree. If you can't bind a port, open the page file directly in a browser, because the site works without a server.

```bash
docs/serve.sh
docs/serve.sh 3221
```

Merging to `main` doesn't publish the site. See Publishing.

- Look at the page at desktop and phone widths. It shouldn't scroll sideways.
- Try the filter and any Data buttons, and check the console for errors.
- Read the text once more against the writing rules above.

## Publishing

The site runs on the `nori-docs` Cloud Run service at docs.noribot.chat. `docs/deploy.sh` builds this folder, without this file, and deploys it. It refuses to publish a changed page that fails `check.py`.

`.github/workflows/docs.yml` can also publish this folder to GitHub Pages, but it stays off unless the repo variable `NORI_DOCS_PAGES` is `true`.
