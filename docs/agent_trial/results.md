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
