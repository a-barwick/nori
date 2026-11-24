# Persona 08: Tariq Al-Mansoor

## Identity & Demographics
- **Name:** Tariq Al-Mansoor
- **Age:** 31 (born May 1995 in Ottawa; Computer Engineering at Waterloo; former Staff PM at Shopify)
- **Location:** Toronto, ON, Canada (`America/Toronto`, `en-CA`) — office on King St. W., lives in Liberty Village with fiancée Yasmin
- **Email:** `tariq.almansoor@sim.nori.local`
- **Role:** Co-founder & CEO of **RelayGrid**, a seed-stage B2B SaaS startup (11 FTEs) building developer workflow & CI/CD bottleneck analytics ($1.42M ARR, 134% NDR, 81.5% gross margin, $148k/mo net burn, ~$2.01M cash / 13.6 months runway).
- **Personal Life:** Engaged to **Yasmin Haddad** (pediatric resident at SickKids Toronto, originally from Montreal). Planning their **June 19, 2027 wedding in Old Montreal** at **Old Port Pavilion** (140 guests).

## Personality & Texture
Tariq operates at 100 mph with three Slack tabs, two investor calls, and a caterer invoice open at the same time. He is sharp, decisive, deeply devoted to Yasmin and his family, and allergic to fluff. When a metric, pricing tier, or pilot blocker changes mid-morning, he does not wait politely for Nori to finish its previous response — he cuts in mid-turn (`"STOP. Ignore that."`, `"actually kill the Veridian task and refocus Aegis on Okta/SAML"`).

He treats Nori like an elite chief-of-staff that can juggle three concurrent background tracks at once — and expects Nori to abort stale drafts immediately when facts change, never let raw pilot debug garbage pollute long-term memory, and never send an outdated investor email after he pivots pricing.

Beneath the founder urgency, he has genuine heart: when Yasmin is coming off a 24-hour SickKids shift, he fiercely protects their wedding details (zabiha halal kitchen compliance for both families, nut-free vegan options for his sister Salma and Yasmin's bridesmaid Chloé, and never missing Maison Zaatar & Olive's $6,200 deposit deadline on Jan 29).

## Writing Style Guide
- **Tone:** Terse, high-urgency, direct, clipped sentences, dashes/bullet fragments, zero pleasantries during work sprints ("stop that", "kill the $39 draft", "ping me the numbers", "need 3 things in parallel").
- **Interruptions & Steering:** Frequently sends an immediate follow-up (`interrupt_active: true`) within 1–2 minutes of a heavy turn to preempt, cancel, or redirect work mid-flight.
- **Parallel Task Bursts:** Regularly dispatches 2–3 concurrent research/drafting tasks in one breath, then selectively stops one while urgently steering (`interrupt=true`) a second and gently appending instructions (`interrupt=false`) to a third.
- **Warm Shift for Wedding/Yasmin:** Slightly softer when writing about Yasmin's residency schedule or family dietary care, though still brisk and action-oriented.

## Cast of Characters & Key Entities
- **Ben Sato (`ben@relaygrid.example`):** Co-founder & CTO of RelayGrid. Brilliant systems engineer, initially pushing for **$39/seat/mo** enterprise pricing to close pilots faster. After a tense Week 1–2 debate, agrees on **Day 9 (Tue Jan 12)** to standardize on **$49/seat/mo** (100-seat minimum, annual commit) so Enterprise ACV clears $60k+ for Series A benchmarks.
- **Series A Raise — $12M USD Target ($48M–$52M pre-money):**
  - **Claire Dupont (`claire@northstarvc.example`):** Partner at **Northstar Ventures**. Frontrunner lead investor; Investment Committee (IC) scheduled for **Tue Jan 26 (Day 23)**; delivers verbal **$12M at $50M pre-money** term sheet on **Day 26 (Fri Jan 29)**.
  - **Graham Whitaker (`graham@mapleleafcap.example`):** General Partner at **MapleLeaf Capital**. Competing term-sheet contender; asks sharp questions on SOC 2 Type II timeline and Enterprise pricing tier. Target of the **Day 9 stale-$39/seat email approval** that Tariq must decline before any send!
- **Three Enterprise Pilots (Noisy Ephemeral Task Data — Must Stay in Daily Logs, NEVER Core Memory!):**
  - **Aegis Bank** — Champion: **Victor Osei** (VP Engineering), 180 seats. Security questionnaire row `SEC-Q-089-TLS13-CIPHER`, Okta/SAML SSO blocker, SOC 2 Type II bridge letter. Converts at $49/seat ($105,840 ARR) on Day 21!
  - **Boreal Logistics** — Champion: **Sonia Tremblay** (Director of Platform), 120 seats. Self-hosted GitHub Enterprise Server runner latency bug `RG-412` (noisy stack marker `oom_killer_worker_pool_v3`). Resolved Day 19.
  - **Veridian Health** — Champion: **Dr. Rajesh Patel** (Chief Architect), 150 seats. Canadian data residency (`ca-central-1`), PHIPA/HIPAA DPA redlines (noisy sandbox audit tag `phipa_sandbox_audit_9941`).
- **Founding Infra Engineer Finalists:**
  - **Maya Kapoor:** Ex-Datadog, Rust/Go + distributed tracing, strong startup ownership; asks **$215k CAD + 0.65% equity**. Glowing references Day 16; Tariq extends winning offer on **Day 18** (**$215k CAD + 0.70% equity**, 4-yr vest / 1-yr cliff). Accepts on Day 22!
  - **Leo Chen:** Ex-Stripe, Kubernetes/ClickHouse specialist; asks **$230k CAD + 0.80% equity**, hesitant about early-stage on-call. Polite pass on Day 18.
- **Wedding — June 19, 2027 (Montreal):**
  - **Fiancée:** **Yasmin Haddad** (SickKids pediatric resident).
  - **Venue:** **Old Port Pavilion** (Quai de l'Horloge, Old Montreal), **140 guests** (114 halal adult, 18 nut-free vegan including sister **Salma** and bridesmaid **Chloé**, 8 halal child).
  - **Dietary Rules:** **100% zabiha halal** kitchen (zero pork/alcohol in prep) + **dedicated nut-free vegan menu** (tree-nut & peanut allergy safety).
  - **Caterer:** **Maison Zaatar & Olive** (Montreal). Deposit schedule: $8,500 venue paid Nov 2026; **$6,200 catering 2nd deposit due Fri Jan 29, 2027**; final balance $11,800 due May 15, 2027.

## 30-Day Narrative Arc (Jan 4 – Feb 2, 2027)
- **Week 1 (Days 1–7, Jan 4–10):** Tariq kicks off his $12M Series A sprint, uploads `series_a_metrics.csv`, introduces the $39 vs $49/seat co-founder pricing battle with Ben, and sets up a weekday 8:00 AM standup brief automation. First **mid-flush preemption** on Day 4 (`interrupt_active: true`) when Aegis Bank's Okta/SAML issue escalates. On Day 6, dispatches **3 concurrent pilot tasks in one breath** (Aegis security questionnaire, Boreal `RG-412`, Veridian DPA), then 2 minutes later selectively **stops Veridian**, **urgently steers Aegis** (Okta/SAML only), and **gently steers Boreal** (append Ben's ETA). Uploads `wedding_guest_dietary.csv` on Sunday while planning with Yasmin.
- **Week 2 (Days 8–14, Jan 11–17):** High-stakes **pricing pivot + stale approval rejection** on Day 9 (Tue Jan 12): Tariq initially asks Nori to prep a MapleLeaf Capital email quoting Ben's **$39/seat** tier; 2 minutes later interrupts (`interrupt_active: true`) when Ben concedes **$49/seat** everywhere, and explicitly **declines the stale approval** (`interaction_reply` `decline`) so NO $39/seat email is sent. Pins `$49/seat` and wedding dietary rules in Memory UI, approves the corrected **$49/seat** email to Claire Dupont on Day 12 (`interaction_reply` `approve`), and cleans up any stale `$39/seat` draft memory.
- **Week 3 (Days 15–21, Jan 18–24):** Heavy Series A data-room burst on Day 15 preempted mid-flush by Maison Zaatar & Olive's **$6,200 catering deposit invoice** (due Jan 29). On Day 17, dispatches **2 concurrent tasks** (Maya vs. Leo hiring scorecard + Northstar IC memo), then interrupts to **stop Leo's comp model** ("we're going with Maya Kapoor") while steering the IC memo to emphasize $49/seat ACV. Extends winning offer to **Maya Kapoor ($215k CAD + 0.70% equity)** on Day 18. Boreal `RG-412` closes (dismisses working item in Memory UI) and **Aegis Bank converts at $49/seat ($105,840 ARR)** on Day 21!
- **Week 4 (Days 22–30, Jan 25–Feb 2):** Maya Kapoor accepts! Pre-IC Day 23 burst preempted by MapleLeaf Capital counter-interest; Day 24 declines a stale IC debrief calendar invite approval when Claire shifts the slot to 4:00 PM. Day 26 (Fri Jan 29): **Northstar Ventures delivers $12M Series A term sheet at $50M pre-money** AND Tariq wires the **$6,200 Maison Zaatar & Olive wedding deposit** on time! Pauses the daily Series A standup automation on Day 28 and runs end-of-month verification proving clean Core Memory (zero `task_outcome` pilot debug leakage, zero stale $39/seat confusion, exact ARR/hiring/wedding facts intact).

## Nori Feature & Failure-Mode Target Map
1. **Mid-Flush User-Arrival Preemption (`interrupt_active: true`):** Four realistic rapid-fire interruptions (Days 4, 9, 15, 23) forcing in-flight flush/turns to yield without advancing `FlushedThrough` prematurely (`INV-MEM-01`, `INV-MEM-05`).
2. **Concurrent Parallel Task Dispatch + Selective `stop` & `steer`:** Days 6 and 17 launch 2–3 concurrent tasks in one prompt, followed by selective cancel + urgent interrupt steer + non-interrupt append steer.
3. **Stale Approval / Interaction Handling (`interaction_reply`):** Day 9 rejects stale `$39/seat` investor email (`expect_no_external_write: true`), Day 12 approves corrected `$49/seat` Northstar email, Day 24 declines outdated calendar slot invite.
4. **`task_outcome` Isolation (`INV-MEM-04` + `must_not_contain_in_core`):** Raw pilot noise (`oom_killer_worker_pool_v3`, `SEC-Q-089-TLS13-CIPHER`, `phipa_sandbox_audit_9941`) lands in Daily Logs (searchable via FTS `expect_fts_query`) and NEVER leaks into Core Memory.
5. **CSV Uploads, Automations UI, Permissions UI, Goals UI & Memory UI:** Exercises full founder power-user stack across 30 days with 7 `settle` checkpoints verifying `INV-MEM-01..08`.
