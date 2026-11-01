# Agent map trial, November 1, 2026

Sonnet 5.5 at medium effort. Each question ran 3 times in a clean clone. Figures are medians per run.

| Feature | Config | Correct | Tokens | Tool calls | Seconds | Cost |
|---|---|---:|---:|---:|---:|---:|
| `find` and map | Baseline | 13/15 | 32,576 | 3 | 11 | $0.029 |
| `find` and map | With feature | 15/15 | 21,618 | 1 | 7 | $0.024 |
| `code` lookup | Baseline | 9/9 | 32,163 | 3 | 9 | $0.025 |
| `code` lookup | With feature | 9/9 | 19,963 | 1 | 6 | $0.008 |
| Stale warning, text only | Baseline | 5/12 | 31,817 | 2 | 9 | $0.031 |
| Stale warning, text only | With feature | 6/12 | 27,803 | 2 | 9 | $0.025 |
| Stale warning, with changed lines | With feature | 12/12 | 34,579 | 2 | 9 | $0.031 |

The stale questions ran against constants seeded to differ from the docs. The changed-lines version was tuned after seeing the text-only result, on the same questions, so 12/12 needs confirming on real drift. Grading is by regular expression on the answer.

Rerun with `python3 docs/agent_trial/run.py OUTPUT_DIR`.

## Web agents, November 1, 2026

Sonnet 5.5 at medium effort, with only web fetch and no repo. Five questions ran 3 times each against the published site, before and after the public build.

| Measure | Before | After |
|---|---:|---:|
| Correct as graded | 1/15 | 9/15 |
| Correct, leaving out the outdated completion question | 1/12 | 9/12 |
| Answers saying the docs lacked the answer | 12 | 1 |
| Median tool calls | 3 | 2 |
| Total cost | $0.241 | $0.197 |

The completion question expected a 0.8 confidence check that milestone 5 removed, so it can't pass, and the question is now updated. The calendar question expected milestone 6, but the site doesn't assign calendar writes to a milestone, so 2 of the 3 misses there are correct answers the grader rejects. Only 2 of 15 runs fetched llms.txt or a Markdown copy, so most of the gain likely comes from the static links in each page's HTML. The site also changed between runs because another update landed, and this measures fetching only, not search ranking.
