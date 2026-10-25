> AGENTS.md and CLAUDE.md are guidance, not law. Use judgment: follow the intent (keep costs low, keep things simple), and do not let a literal reading block reasonable work or add process. When a rule and good sense conflict, do the sensible thing and say so in one line.

# GCP cost rules

These apply to this repository's GCP project `bot-111549`. They are cost controls, not design constraints.

- Keep Cloud Run and Agent Runtime minimum instances at zero and maximum instances at one.
- When you finish live work, scale worker pools to zero and stop the computer. Don't leave paid compute running.
- Never delete, replace, or rebuild retained user data (disks, browser profile, files) to save money.
