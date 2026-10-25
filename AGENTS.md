# GCP cost rules

These apply to this repository's GCP project `bot-111549`. They are cost controls, not design constraints.

- Keep Cloud Run and Agent Runtime minimum instances at zero and maximum instances at one.
- When you finish live work, scale worker pools to zero and stop the computer. Don't leave paid compute running.
- Never delete, replace, or rebuild retained user data (disks, browser profile, files) to save money.
