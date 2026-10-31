# Writing Nori docs pages

This folder is the Nori docs site. Read this file before adding or changing a page. Follow these rules unless the repo owner says otherwise.

## What a page should feel like

A reader should get the idea from the picture, understand it from a few short lines, and only open details if they want them. Write the way you'd explain it to a friend who is a good engineer.

1. **Visual first.** Open with a diagram, a state flow or a compact table. If a page has no visual, ask what it should be before writing more text.
2. **Easy to scan.** Use short lists and tables. Keep paragraphs to one or two sentences.
3. **Details stay hidden until asked for.** Put depth in a Data button or a `details.more` section.
4. **Minimal UI.** Add a control or label only if the page needs it.

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
<details class="sources"><summary>Sources</summary><p>Links to Linear issues and repo files.</p></details>
</body>
</html>
```

Then add the page to `NAV` in `site.js`. The script adds the navigation, previous and next links, section lists, copy links and the filter. Don't copy those into a page.

- Keep each page to one topic. Link to another page instead of repeating it.
- Each client gets one `<Platform> client` page in the Clients group. Rules that apply to every client go on `clients.html` or `api.html`.
- Split a page that passes about 800 words into pages by topic. Reference pages such as Scenarios and Decisions can run longer.
- Don't list every value just because the code has it. Put long lists of tokens, limits or fields in a `details.more` section.
- Every heading gets a copy-link icon automatically. Only the icon is interactive, so don't make headings or subtitles clickable. Link IDs come from the heading text, so renaming a heading breaks old links to it.
- Use relative links such as `tasks.html`, because the site won't always be hosted at the root of a domain.
- Use what's already in `site.css`: `card`, `grid2`, `grid3`, `flow`, `node`, `branch`, `states`, `say`, `tag` and `pts`. Add new CSS only when a page needs something those can't do.
- Don't add a framework, a build step or external scripts without asking first.

## Diagrams

- Prefer `.flow` and `.node` HTML diagrams because they adjust to the screen width. Use inline SVG when position carries meaning.
- Keep the colors consistent. Blue is the outer loop, green is the inner loop and Temporal, orange is the computer, gray is the runtime and storage, and yellow is waiting.
- Label every arrow whose meaning isn't obvious, and don't let lines cross boxes or text.

## Built and planned

Readers must always be able to tell what runs today.

- Mark anything not on `main` with `class="designed"`. Mark uncommitted work with `class="in-review"`. The filter finds both.
- Add `<span class="tag design">designed</span>` where the marking alone isn't clear.
- A page that is entirely planned goes in the Planned group in `NAV`, which labels it automatically.
- If a marked sentence is hidden, the text around it must still read properly.

## Facts

When sources disagree, the code on `main` decides what's built. Linear and `decisions.html` decide what's planned.

- Check limits, names, endpoints and tools against the code before writing them down. Don't copy them from an older doc.
- If a source is out of date, follow the code and add the conflict to `sources.html`.
- Date any cost or measurement, and say that it's a planning figure if it is.
- Never include secrets, API keys, personal email addresses, or real user messages and task IDs. The site may be public.
- Link Linear issues as `AB-123`, and list the sources at the bottom of the page.
- Record each new design decision in `decisions.html`, with its date and what was rejected. Add unresolved questions to `open-questions.html`, and remove them once settled.

## Data buttons

A Data button shows the request, response and saved record behind a step or a table row.

- Add `data-flow="key"` to a table row, add the key to `window.FLOWS` in `task-data.js`, and load that script before `site.js`. Request page steps read `request-data.js`.
- Take endpoints and field names from the code, and set `source` to the file they came from.
- Values are invented, so use made-up examples. Mark anything shortened with `…`.
- Don't add a Data button to something that isn't built yet, because there's no code to base it on.

## Before you finish

Run the site from the repo root. It opens at http://127.0.0.1:3220:

```bash
docs/serve.sh
```

- Look at the page at desktop and phone widths. It shouldn't scroll sideways.
- Try the filter and any Data buttons, and check the console for errors.
- Read the text once more against the writing rules above.

## Publishing

The site runs locally with `docs/serve.sh` and isn't hosted right now. `.github/workflows/docs.yml` can publish this folder, without this file, but it stays off unless the repo variable `NORI_DOCS_PAGES` is `true`. Don't commit unfinished pages to `main`.
