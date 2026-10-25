# GCP cost and lifecycle rules

These rules apply to this repository's GCP project `bot-111549`.

- An explicitly authorized task execution session may last at most three hours. Install automatic shutdown so resources cannot keep running if Austin walks away for days. Expiry never authorizes an unattended restart; a new session requires explicit authorization.
- Install and verify the cloud-side shutdown deadline before starting paid capacity or opening task admission. Keep the task admission expiry early enough to finish cancellation and cleanup before that deadline.
- Keep worker and computer capacity off outside an authorized task execution session. Do not add unattended starts, resumes, restarts, scheduled starts, or background wakeups.
- Pause task execution at session end: close durable session control, cancel active work, revoke WCI pool access and disable its invoker, scale every retained worker pool to zero, and stop the retained computer. Verify shutdown and WCI access denial before reporting that execution is paused.
- Keep the owner-only website and saved history accessible after pause or expiry. Resume and pause must preserve the existing owner IAP, IAP service-agent invocation, and web runtime-query grants; durable paused/expired control blocks new task execution. Preserve native IAP and exact owner-email authorization.
- Keep Cloud Run and Agent Runtime minimum instances at zero and maximum instances at one.
- Never delete, replace, reformat, or rebuild retained user data to reduce costs. Stopping compute must preserve the retained guest disk, browser profile, files, and other persisted state.
- Report storage charges that remain after pause, including retained disks, objects, images, and database data. Use current inventory and pricing when estimating amounts; state any unknown costs.
