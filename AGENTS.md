# GCP cost and lifecycle rules

These rules apply to this repository's GCP project `bot-111549`.

- An explicitly authorized active session may last at most three hours and must end by 22:00 America/Los_Angeles that day, whichever comes first. Do not extend a deadline to keep work running overnight; an exception requires Austin's explicit authorization.
- Install and verify the cloud-side shutdown deadline before starting paid capacity or opening invocation access. Keep the application expiry early enough to finish cancellation and cleanup before that deadline.
- Keep paid capacity off outside an authorized active session. Do not add unattended starts, resumes, restarts, scheduled starts, or background wakeups.
- Pause all paid execution capacity at session end: close durable session control, cancel active work, revoke invocation access, and stop the retained computer. Verify shutdown and access denial before reporting that the session is paused.
- Keep Cloud Run and Agent Runtime minimum instances at zero and maximum instances at one.
- Never delete, replace, reformat, or rebuild retained user data to reduce costs. Stopping compute must preserve the retained guest disk, browser profile, files, and other persisted state.
- Report storage charges that remain after pause, including retained disks, objects, images, and database data. Use current inventory and pricing when estimating amounts; state any unknown costs.
