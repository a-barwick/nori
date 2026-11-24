# Persona Bible: 04 — Carlos Mendoza

## 1. Identity & Demographics
- **Full Name:** Carlos Mendoza
- **Age:** 51
- **Occupation:** Mechanical Engineering Director, **FrontRange Robotics** (Golden/Boulder corridor, Colorado)
- **Location & Timezone:** Lakewood / Denver metro, Colorado (`America/Denver`, `en-US`)
- **Email:** `carlos.mendoza@sim.nori.local`
- **Family & Social Circle:**
  - **Elena Mendoza:** Wife, pediatric physical therapist, co-manages household budgets (established a **$2,800** spring campus-visit budget cap for Sofia).
  - **Sofia Mendoza (17):** Daughter, senior and robotics club captain at Lakewood High School. Applying to Mechanical/Aerospace Engineering at **MIT**, **Georgia Tech**, **CU Boulder** (admitted early), and **Cal Poly San Luis Obispo** (admitted Jan 29 with `$4,000/yr` merit grant). Has a **peanut allergy**.
  - **Hector Alvarez (`hector.alvarez@sim.nori.local`):** Brother-in-law in Albuquerque, drives a 2022 Toyota Tacoma TRD Pro; longtime backcountry overland partner for April's 10-day Utah trip.

## 2. Personality & Texture
Carlos is steady, warm, deeply proud of his daughter, practical to the bone, and occasionally flares up when a vendor threatens his hardware build schedule before cooling off and thinking through bridge risk like a seasoned engineering leader. He loves his 2023 Toyota 4Runner TRD Off-Road, green chile stew over a camp stove, and clean tolerance stackups. He treats Nori as a dependable chief of staff who can untangle his sprawling voice-note dumps into exact dates, hard financial tables, and cleanly tracked goals — while ALWAYS checking with him before firing off external emails to suppliers or family.

## 3. Writing Style Guide
- **Warm, slightly formal, voice-note cadence:** Writes long, flowing conversational paragraphs that jump smoothly from actuator tolerances to Sofia's college essays to 4Runner brake pads, burying two or three action items inside a single message.
- **Heavy use of relative dates:** Habitually speaks in relative timeframes (*"two weeks from today"*, *"by next Thursday afternoon"*, *"two weeks from tomorrow"*, *"the Friday after next"*, *"five days from now"*, *"two weeks from yesterday"*), relying on Nori to normalize them into unambiguous January/February 2027 calendar dates across compaction and AutoDream.
- **Human reversals:** Capable of impulsive frustration (requesting a cancellation email to Apex Precision after Wei Lin's lead-time ultimatum) followed by grounded reconsideration (declining the approval card once CFO Rachel Cho points out Q2 bridge inventory risk).

## 4. Cast of Characters & Core Entities
- **Part #FR-ACT-402 (Series-4 Harmonic Actuator Housing):** Core robot joint component (~12,000 units/year run rate) transitioning from China to Mexico nearshoring.
- **Apex Precision Machining (Shenzhen, China — Contact: Wei Lin, `wlin@apexprecision.example`):** Incumbent Tier-1 supplier. `$44.80` unit price, 1,500-unit MOQ, tooling already amortized (`$0` NRE), **25% Section 301 tariff**, `$4.90/unit` ocean freight (`$60.90` landed/unit), `$21.50/unit` air expedite (rejected as too expensive). Lead time slipped from **6 weeks to 9 weeks** (threatening 10 weeks unless a `$65k` billet deposit is wired). Retained strictly for a single 1,500-unit Q2 bridge batch until Taller Azteca clears PPAP.
- **Taller Azteca Industrias (Guadalajara, Mexico — Contact: Mateo Ruiz, `mruiz@tallerazteca.example`):** Prospective nearshore Tier-1 supplier. `$51.20` unit price, 1,000-unit MOQ, **USMCA duty-free (0% tariff)**, `$1.65/unit` truck freight (`$52.85` landed/unit), 3.5-week lead time, ISO 9001 & AS9100D certified. **`$118,000` tooling NRE** negotiated from 50% upfront down to a **40% / 40% / 20% milestone split** (`$47,200` PO deposit / `$47,200` T1 sample / `$23,600` PPAP approval). Mateo visits Golden lab **Jan 22, 2027**; PPAP targeted **May 14, 2027**.
- **Rocky Mountain CNC Works (Broomfield, CO — Contact: Dana Kowalski, `dkowalski@rmcnc.example`):** Local domestic emergency backup only (`$74.50/unit`, `$42k` tooling, viable only for a 300-unit emergency bridge).
- **Rachel Cho (`rcho@frontrangerobotics.example`):** FrontRange Robotics CFO. Chairs the **Q1 CapEx Board Review on Monday, February 1, 2027**, which approves the `$118k` Taller Azteca tooling PO and Apex Q2 bridge order.
- **Dr. Greg Hollis:** MIT alumni interviewer who meets Sofia in Belmar on **Wednesday, January 20, 2027**.
- **Utah White Rim Trail / Canyonlands Overland Trip (April 9–18, 2027):** 10-day backcountry trip with Hector. Recreation.gov permit window opens **Monday, January 18, 2027 at 08:00 AM MT** -> secured permit **`#NPS-CANY-48291`** (`$95` total; Hector owes `$47.50`) for clockwise campsites **Gooseberry A (Apr 11)**, **White Crack (Apr 12)**, **Murphy Hogback B (Apr 13)**, and **Taylor Canyon / Potato Bottom A (Apr 14)**. Shared budget cap: **`$1,450`** (`$725` each). 4Runner front brake pads due March 2027.

## 5. 30-Day Narrative Arc
- **Week 1 (Days 1–7):** Introduces his engineering, college-app, and Utah overland threads; creates two tracked Goals via `goal_ui`; uploads `supplier_comparison.csv` and `utah_overland_gear.txt`; drops early relative dates (Jan 14 payback deck, Jan 18 CSS Profile, Jan 22 CU Boulder Honors essay) and Sofia's peanut allergy.
- **Week 2 (Days 8–14):** Drops relative dates for Mateo's Golden lab visit (Jan 22), Sofia's MIT interview (Jan 20), and Hector's permit dry-run call (Jan 18); drafts and **approves** (`interaction_reply`) an email to Mateo Ruiz proposing a 40/40/20 tooling payment split; receives Mateo's acceptance; runs parallel supplier-tradeoff and family-budget subtasks with mid-task steering (adding 15% tariff sensitivity and excluding air expedite >`$18/unit`).
- **Week 3 (Days 15–21):** Wins all four primary White Rim campsites (`#NPS-CANY-48291`) and updates both Goals; drops relative date for the Q1 CapEx Board Review (`two weeks from yesterday` -> Feb 1, 2027); receives Wei Lin's lead-time ultimatum, impulsively requests an Apex cancellation email, then **declines** (`interaction_reply`) the outbound write after speaking with Rachel Cho so FrontRange keeps a Q2 bridge batch; hosts Mateo Ruiz in Golden on Jan 22 and celebrates Sofia's CU Honors essay submission.
- **Week 4 (Days 22–30):** Verifies all five relative dates normalized to exact calendar dates post-AutoDream; builds and revises the Library CapEx Board Brief with exact 40/40/20 tranches (`$47,200/$47,200/$23,600`); **approves** (`interaction_reply`) sending locked White Rim permit details to Hector; celebrates Sofia's Cal Poly SLO acceptance (`$4,000/yr` merit) and unanimous Board approval on Feb 1; completes end-of-month goal, approval-governance, and relative-date audits.

## 6. Nori Feature & Failure-Mode Target Map
- **Relative-Date Normalization across `flush_and_compact` & `AutoDream`:** Five distinct relative-date anchors planted in voice-note chats (Days 1, 4, 8, 10, 16) and rigorously probed in Weeks 2, 3, and 4 for exact resolved January/February 2027 dates.
- **Multi-Week Goals (`goal_ui`):** Two concurrent personal/family goals created and updated across the month (`Sofia College Applications & Financial Aid 2027`, `Utah White Rim Overland Trip — April 2027`).
- **Approval / Interaction Cards (`interaction_reply`):** Real approve/decline governance — approving Mateo Ruiz RFQ terms (Day 10), **declining** Apex Precision cancellation after a change of mind (Day 17, verified with `expect_no_external_write: true`), and approving Hector's White Rim confirmation email (Day 24).
- **Parallel Subtasks, Mid-Task Steering & Shared Family Budgets:** Combined supplier payback modeling + tariff sensitivity steering + combined Q1/Q2 household budget tracking (`$2,800` campus visits + `$725` Utah share).
- **File Uploads & FTS Recall:** `assets/supplier_comparison.csv` and `assets/utah_overland_gear.txt` ingested and verified via cross-week FTS probes.
- **Coupled 3-Tier Memory Invariants:** Six `settle` checkpoints asserting `INV-MEM-01..08` across all four weeks.
