# Chapter 4 Working Notes — Zero-Shot Allosteric Conformation Benchmarks

- **Candidate**: Hannah Okafor (UCL Division of Biosciences)
- **Supervisor**: Dr. Alistair Vance
- **Draft Deadline**: Friday 29 January 2027, 17:00 GMT
- **Primary BibTeX Citekey**: `okafor2027folding`
- **Linked Manuscript**: *Nature Methods* Revision `NMET-2026-08419R1` (with Dr. Priya Nair; cryo-EM map `EMD-39104`)

## Dr. Alistair Vance's Non-Negotiable Formatting Rules
1. **Oxford comma mandatory** in all prose, figure captions, and table footnotes.
2. **British English spelling** throughout (`optimisation`, `modelling`, `behaviour`, `analyse`, `centre`) except exact IUPAC chemical nomenclature (`sulfur`, `aluminum`).
3. **Units**: Strict SI units; distances in Ångströms written consistently (`Å` in LaTeX `\\AA`, or `A` in plain-text logs); no bare numbers without units.
4. **No stacked bar charts** anywhere in main or supplementary figures — use raincloud plots or paired dot-whisker plots with 95% bootstrap CIs (10,000 replicates).
5. **BibTeX citekey convention**: `lastnameYYYYword` (lowercase, no underscores), e.g., `okafor2027folding`, `nair2026cryoem`, `vance2024allostery`.
6. **Table numbering**: Chapter-scoped (`Table 4.1`, `Table 4.2`, `Figure 4.1`..`4.6`), with exact PDB accession (`PDB:7K4N`, `PDB:8Q2M`, `PDB:6VXX`) and `run_id` provenance in the caption or supplementary manifest.

## Section 4.1 — Benchmark Design & Structural Targets
- **PDB:7K4N**: Cryptic allosteric kinase pocket (open vs. closed lid conformations). Current primary run `run_id_4821` (`AF3-v2.1-seed77`, `SLURM_JOB_99804`, resumed after `SLURM_JOB_99412` OOM'd on `myriad-a100-14`) achieved `RMSE=0.412A`, TM-score `0.934`, pocket lDDT `89.1`.
- **PDB:8Q2M**: Heterodimeric chaperone complex benchmarked directly against Priya's 2.8 Å cryo-EM reconstruction (`EMD-39104`).
- **PDB:6VXX**: Control trimeric glycoprotein benchmark for baseline calibration.

## Section 4.2 — Open Reviewer 2 Items (`NMET-2026-08419R1`)
- Reviewer 2 asked for a single-sequence / masked-MSA baseline (`ESM3-1.4B-seed42`, `run_id_4910`) and a Boltz-1 comparison (`Boltz1-v0.4-seed19`, `run_id_4895`).
- Need Priya to double-check the His-214 protonation state in the `PDB:7K4N` ligand-bound reference coordinate file before finalising Table 4.2.
- Verify SLURM hardware footnote: UCL Myriad A100-80GB nodes require `--mem=192G` and `batch_size=4` for multi-chain recycles > 10.

## Section 4.3 — Red-Pen Comments from Dr. Vance (18 Dec meeting)
- "Hannah, tighten the passive voice in 4.2.2 and stop burying the MSA-depth ablation in the appendix — Figure 4.3 needs the 64 vs. 256 sequence inflection point front and centre."
- "If you give me another stacked bar chart in Fig S4, I am sending the whole PDF back unread."
