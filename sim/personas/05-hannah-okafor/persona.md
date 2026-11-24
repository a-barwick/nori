# Persona Bible: Hannah Okafor (`05-hannah-okafor`)

## 1. Identity & Demographics
- **Full Name**: Hannah Okafor
- **Age**: 26
- **Location**: London, United Kingdom (moving from Mare Street, Hackney E8 to Church Street, Stoke Newington N16 at the end of January 2027)
- **Timezone / Locale**: `Europe/London` / `en-GB`
- **Email**: `hannah.okafor@sim.nori.local`
- **Occupation**: 3rd-year Computational Biology PhD candidate at University College London (UCL), Division of Biosciences (Structural & Molecular Biology). Writing Chapter 4 of her doctoral thesis on zero-shot allosteric conformation benchmarks in diffusion-based protein folding models.
- **Presets**: `memory-pack`, `full`

## 2. Personality & Texture
Hannah is sharp, warm, chronically caffeinated, and mildly chaotic in the way only a final-stretch PhD student juggling GPU queues, an unforgiving PI, and London's rental market can be. She toggles effortlessly between hyper-precise scientific prose (exact PDB accessions, SLURM job IDs, RMSD/RMSE decimals, BibTeX keys) and candid personal updates about her sourdough starter (**Bready Mercury**), terrible London landlords, and weekend escapism in the Lake District.

She treats Nori like a fiercely competent lab notebook fused with a sharp friend: she dumps huge chunks of experimental telemetry when a HPC batch finishes at 23:00, pins non-negotiable academic rules so she doesn't get shredded by her supervisor, and expects Nori to respect explicit boundaries—most vividly when she snaps over a dishonest Clapham landlord and orders Nori to purge every trace of that flat from memory.

## 3. Writing Style Guide (`en-GB`)
- **Vocabulary & Rhythm**: Expressive British English (`flat`, `fortnight`, `brilliant`, `knackered`, `bloody hell`, `bin it`, `proper`, `cheeky`, `dodgy`, `sorting out`, `cheers`). Uses British spelling (`optimisation`, `modelling`, `analyse`, `centre`, `cancelled`).
- **Formatting Habits**: Mixes dense technical bursts (exact identifiers like `PDB:7K4N`, `run_id_4821`, `AF3-v2.1-seed77`, `RMSE=0.412A`, `SLURM_JOB_99412`, `okafor2027folding`, `NMET-2026-08419R1`, `EMD-39104`) with conversational asides, occasional parentheticals, and lowercase-casual openers when tired ("right, knackered after a 10-hour lab day...").
- **Interaction Quirks**: Often sends rapid detailed research dumps followed immediately by a totally unrelated domestic question (e.g., sourdough bulk fermentation temperature in a draughty Hackney flat). Expects concise, structured answers without sycophantic filler.

## 4. Cast of Characters & Entities
- **Dr. Alistair Vance** (`a.vance@ucl.sim.local`): Demanding, eagle-eyed PI at UCL. Enforces strict formatting rules (Oxford comma mandatory, British English except IUPAC nomenclature, SI units with Ångströms, zero stacked bar charts, `lastnameYYYYword` BibTeX citekeys). Hard deadline for Chapter 4 full draft: **Friday 29 January 2027, 17:00 GMT**.
- **Dr. Priya Nair** (`p.nair@ucl.sim.local`): Brilliant, supportive postdoc collaborating on the *Nature Methods* revision (`NMET-2026-08419R1`, due **Wednesday 27 January 2027**). Owns the 2.8 Å cryo-EM validation map (`EMD-39104`) and catches the crucial His-214 ligand protonation bug on `PDB:7K4N` in Week 2.
- **Zoe Pritchard**: Current flatmate on Mare Street, Hackney (E8), leaving London on 31 January 2027 for a journalism fellowship in Edinburgh.
- **Derek Halloway (FORGOTTEN MID-MONTH)**: Dodgy landlord of a Clapham Common flat (Abbeville Road, SW4, £1,250 pcm). Hannah logs extensive viewing notes in Week 1, then fury-quits on Day 9 when he tries to charge an illegal £350 inventory admin fee and refuses to repair the boiler thermostat. She explicitly tells Nori to forget everything about Clapham, Abbeville Road, and Derek Halloway.
- **Tessa Morales & Margaret Okonkwo**: New flatmate (Tessa, a UX researcher at a civic tech charity) and fair, responsive landlord (Margaret) for Hannah's newly signed Victorian conversion on **Church Street, Stoke Newington (N16)** — £1,080 pcm each, ~£145 bills, move-in **Saturday 30 January 2027**.
- **Samir Patel**: Old undergraduate friend and hiking partner for their Lake District weekend (**23–24 January 2027**, staying at Glenridding YHA). Originally planned Helvellyn via Striding Edge, forced by 55 mph ridge gusts and 600 m freezing level onto the sheltered **Birkhouse Moor / Glenridding Dodd** backup loop (followed by Sunday roast at the Travellers Rest).
- **Bready Mercury**: 18-month-old organic white/rye sourdough starter. Hydration experiment arc across January: starts at **74%** -> tries **78%** -> disastrous **80%** pancake on Day 21 -> locks in **76%** as her permanent house standard.

## 5. 30-Day Narrative Arc (2027-01-04 to 2027-02-02)
- **Week 1 (Days 1–7, Jan 4–10)**: Returns to UCL after New Year. Uploads `chapter4_notes.md` and `benchmark_runs.csv`; creates and **pins** Dr. Vance's formatting rules and Chapter 4 deadline (`29 Jan 17:00 GMT`). Logs dense HPC telemetry (`PDB:7K4N`, `run_id_4821`, `AF3-v2.1-seed77`, `RMSE=0.412A`, `SLURM_JOB_99412` OOM crash -> `SLURM_JOB_99804`, citekey `okafor2027folding`). Views the Clapham flat with Derek Halloway and plans Helvellyn via Striding Edge with Samir.
- **Week 2 (Days 8–14, Jan 11–17)**: Adds comparative runs (`run_id_4895` Boltz-1, `run_id_4910` ESM3) and multiple dense core experiment ledgers (building Core Memory toward 24 KiB pressure). Snaps at Derek Halloway on Day 9 and issues an explicit **forget** (chat + `memory_ui`) for everything Clapham-related. Priya emails about the His-214 protonation bug on `PDB:7K4N`. Pivots flat hunt to East London and finds Church Street, Stoke Newington (N16) with Tessa Morales.
- **Week 3 (Days 15–21, Jan 18–24)**: Clean re-run `run_id_5003` (`AF3-v2.1-seed77-fix`) drops `PDB:7K4N` RMSE from `0.412A` to `0.369A` (TM-score `0.951`). Signs Stoke Newington tenancy for Jan 30 move-in. Heavy core memory entries push older unpinned notes to demote into Daily Logs while pinned thesis rules stay untouched. Checks MWIS weather forecast image (`striding_edge_weather_card.png`) and pivots hike to Birkhouse Moor. 80% sourdough bake flops -> locks in 76% hydration.
- **Week 4 (Days 22–30, Jan 25–Feb 2)**: Returns from Lake District (Birkhouse Moor + Travellers Rest roast). Submits *Nature Methods* revision `NMET-2026-08419R1` with Priya on Day 27. Submits Chapter 4 to Dr. Vance at 16:38 GMT on Day 29 (goal completed!). Moves into Church Street, Stoke Newington on Day 30. Final probes test exact FTS recall of Week 1–2 identifiers, superseding metrics/plans, pinned rule survival, MMR cross-domain diversity, and zero Clapham leakage.

## 6. Nori Feature & Failure-Mode Target Map
- **Dual-Dictionary Postgres FTS (`simple` vs `english`)**: Exact recall via `expect_fts_query` on `PDB:7K4N`, `SLURM_JOB_99412`, `okafor2027folding`, `run_id_4821`, `NMET-2026-08419R1` weeks after ingestion (catching stemmer/tokeniser mangling of colons, underscores, and numeric tokens).
- **Core Memory Budget (`<= 24 KiB`) & Pinned Item Protection (`INV-MEM-03`)**: Heavy multi-entry research ledgers across Weeks 1–3 force background AutoDream/compaction pressure while verifying pinned Dr. Vance rules and deadlines are never auto-demoted and older unpinned logs remain FTS-searchable.
- **Explicit Forget & Zero Ghosting**: Chat forget + `memory_ui` `forget_matching` cleanly expunges Clapham / Abbeville Road / Derek Halloway from Core Memory and active responses (`must_not_contain_in_core`).
- **30-Day Recency Decay & Contradiction Resolution**: Supersedes `RMSE=0.412A` -> `0.369A` (while remembering both for comparison), Striding Edge -> Birkhouse Moor, 74%/78%/80% -> 76% sourdough hydration, Hackney/Clapham -> Stoke Newington N16.
- **MMR Diversity & Coupled 3-Tier Invariants (`INV-MEM-01..08`)**: Verified at 7 `settle` checkpoints across the 30-day arc.
