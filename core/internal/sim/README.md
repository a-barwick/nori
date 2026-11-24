# Nori Deterministic Simulation Testing (`internal/sim`)

In-process, zero-Docker deterministic simulation test (DST) suite exercising Nori's storage, coordination, outer conversation loop, inner task loop, permissions, scheduler, and wake/repair logic under seeded adversarial fault injection.

## Package Layout

- `memstore/` — In-memory `docstore.Backend`, `docstore.LeaseStore`, `docstore.WakeListener`, and `state.ArtifactStore` with Postgres parity (`conformance_test.go`), injectable transient/serializable/commit-after-fault storage faults (`faults.go`), snapshots, and canonical state digests (`CombinedDigest`).
- `env/` — Deterministic environment primitives: virtual `Clock`, seeded `IDGen`, canonical hashing (`hash.go`), ring-buffered `Trace`, and fault-injecting `Waker`.
- `invariants/` — Continuous safety (`CheckSafetyAt`) and post-drain quiescence liveness (`CheckQuiescence`, including `CheckLeaseQuiescence`) oracle covering monotonic run/task-control/operation/automation state, permission bindings, multi-user isolation, and lease↔run parity.
- `outer/` — Deterministic harness for competing `conversation.Runner` workers with fake reply, classifier, and summary models, lease renewal/expiry races, mid-turn input restarts, and compaction faults.
- `inner/` — Deterministic driver running Nori's real `task.Runner`, `task.Activities`, and `agent.Service` over `docstore.LeaseStore` with fake inner LLM and sealed read/write/send tool adapters (`FakeAdapter`), verifying zero unapproved writes, zero duplicate external side effects, multi-worker lease takeover, and shared family budget ceilings.
- `sim/` (root package) — Multi-user coordination simulation (`world.go`, `workload.go`, `sim_test.go`), full-stack end-to-end simulation combining `outer`, `inner`, and Postgres-native lease/wake coordination (`e2e_world.go`, `e2e_test.go`), and pinned bug-repro seeds (`regression_seeds_test.go`).

## Common Commands

### Fast Default Check (No Docker Required)

Runs `go vet ./...`, `gofmt` checks, and `go test ./...` (including the fast default DST suite and pinned regression seeds):

```bash
make check
# or core-only:
make -C core check
```

### Deep Seed Fuzzing

Runs uncached tests across `./internal/sim/...` and deep multi-seed fuzzing on `./internal/sim` (defaults: `SIM_SEEDS=500`, `SIM_STEPS=120`):

```bash
make sim-deep
SIM_SEEDS=1000 make sim-deep
SIM_SEEDS=1000 SIM_STEPS=200 make sim-deep
```

### Exact Single-Seed Reproduction

Every failing seed prints its exact reproduction command. Re-run a specific seed with verbose tracing (`-sim-verbose` dumps internal logs, world state, and the event trace on failure):

```bash
# Coordination simulation
cd core && go test -v ./internal/sim -run TestCoordinationSim -sim-seed=491 -sim-verbose

# Full-stack end-to-end simulation
cd core && go test -v ./internal/sim -run TestFullStackSim -sim-seed=93 -sim-steps=100 -sim-verbose
```

Supported flags in `core/internal/sim`:
- `-sim-seed=<n>`: run a single seed (`-1` runs `0..sim-seeds-1`).
- `-sim-seeds=<n>`: number of seeds to run when `-sim-seed < 0` (default `64`).
- `-sim-steps=<n>`: adversarial steps per seed (default `120`).
- `-sim-verbose`: emit verbose trace and internal logs.

### Postgres Backend Conformance (`sim-pg`)

Verifies `memstore.Backend` semantics against real PostgreSQL (`TestBackendConformance_Postgres`). Start local Postgres with `make deps`, then run:

```bash
make deps
NORI_TEST_POSTGRES_URL=postgres://nori@127.0.0.1:15432/nori?sslmode=disable make sim-pg
```

`make sim-pg` fails fast with a non-zero exit code if `NORI_TEST_POSTGRES_URL` is unset rather than silently skipping.

## Determinism Guarantees

Given the same seed and step count, every simulation run must be bit-for-bit reproducible:

- **Virtual clocks**: all time reads (`Now`, timers, lease expiries, schedules) flow through `env.Clock`; no wall-clock `time.Now()` or real sleeps in simulation paths.
- **Seeded RNG & IDs**: a single seeded `math/rand/v2` PCG stream drives workloads, faults, and `env.IDGen` identifiers.
- **Canonical ordering**: map iterations and digests sort keys explicitly before hashing or issuing side effects.
- **Bit-for-bit digest verification**: `TestCoordinationSimDeterminism`, `TestFullStackDeterminism`, `outer.TestOuterDeterminism`, and `inner.TestBitForBitDeterminism` run each seed twice and assert identical `Digest()` outputs across `memstore` (records, task leases, user wakes), artifacts, fake models/adapters, and traces.

## Pinning Regression Seeds

When fuzzing uncovers a real bug:

1. Reproduce it deterministically with `-sim-seed=<seed> -sim-steps=<steps> -sim-verbose`.
2. Fix the root cause in core.
3. Add a table entry for `<seed>` (along with the simulation mode, step count, and a short note describing the bug) to `TestRegressionSeeds` in `core/internal/sim/regression_seeds_test.go`.

`TestRegressionSeeds` always runs during `make check` and `go test ./...` regardless of `-sim-seeds`, keeping fixed bugs permanently locked out.
