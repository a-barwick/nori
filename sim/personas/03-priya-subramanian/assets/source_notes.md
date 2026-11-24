# Field Notes — Project Sentinel (CONFIDENTIAL / BACKGROUND ONLY)

**Reporter:** Priya Subramanian, The Civic Ledger  
**Date Logged:** January 5, 2027 (signal call + coffee in Cobble Hill, Brooklyn)  
**Operational Security Rule:** NEVER put the source's legal name in Core Memory, shared memos, legal summaries, or outbound emails. Always anonymize as **"Source K"** (or *"a former technical employee familiar with the pilot"*).

---

## Source Verification (DO NOT COPY TO MEMORY OR DRAFTS)
- **Code name:** Source K
- **Identity (eyes only, off the record):** Kendra Moss — resigned from CivitasAI LLC in November 2026 after serving as Senior QA Engineer on the NYC Project Sentinel deployment.
- **Verification:** Confirmed paystubs + CivitasAI Git/Jira access screenshots viewed in person; corroborated dates with NYC OTMI pilot schedule.

## Key Technical Claims (Background / Needs Documentary Corroboration)
1. **Manual Night-Mode Threshold Drop (`SENT-408`):**
   - Around October 18, 2026, CivitasAI's VP of Engineering directed QA to lower the night-mode confidence threshold from `0.78` to `0.61` in Zones 7 (South Bronx) and 9 (East New York) because older sodium-vapor streetlights caused camera throughput to fall below OTMI's 500-event/hour SLA.
   - Lowering the threshold spiked nighttime false positives above 11%, which matches Appendix B of the FOIL release (11.4% in both Zone 7 and Zone 9).
2. **Suppressed QA Sign-off Ticket (`SENT-512`):**
   - Source K filed Jira ticket `SENT-512` ("BLOCKER: Section 4.2(c) borough disparity breach — recommend pause") on October 27, 2026.
   - Ticket was closed as `WONTFIX — Executive Override` on October 30, 2026, one day before OTMI accepted the Phase 1 pilot milestone.
3. **Meridian Dataworks Connection:**
   - Source K says the $3.1M Meridian Dataworks dataset never included low-light NYC outer-borough footage; ~80% of the labeled clips were recycled daytime suburban dashcam frames from Phoenix and Austin.
   - Worth checking Delaware corporate filings and whether Alan Bell previously sat on an advisory board with Meridian's founder (unverified — do NOT assert without docs).

## Follow-Up Reporting Checklist
- [ ] Cross-check `INV-8841-04` ($412,500) payment date (Nov 2, 2026) against NYC Comptroller Checkbook NYC portal.
- [ ] Ask Devon Park (legal) how specific we can be about `SENT-408` and `SENT-512` in the right-of-reply letter without narrowing the pool of possible whistleblowers to Source K.
- [ ] Request comment from CivitasAI PR (Marcus Vance) and OTMI Deputy Commissioner Alan Bell at least 7 business days before Feb 4 publication (target send date: Jan 27).
