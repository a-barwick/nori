package sim_test

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"nori/internal/sim"
)

var (
	simSeed    = flag.Int64("sim-seed", -1, "single simulation seed to run (-1 runs 0..sim-seeds-1)")
	simSeeds   = flag.Int("sim-seeds", 64, "number of seeds to run when -sim-seed < 0")
	simSteps   = flag.Int("sim-steps", 120, "number of adversarial steps per seed")
	simVerbose = flag.Bool("sim-verbose", false, "emit verbose trace and internal logs")
)

func silenceStdLog(t *testing.T) {
	t.Helper()
	if *simVerbose {
		return
	}
	orig := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		log.SetOutput(orig)
	})
}

func configForSeed(_ int64) sim.WorldConfig {
	return sim.WorldConfig{}
}

func TestCoordinationSim(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	var seeds []int64
	if *simSeed >= 0 {
		seeds = []int64{*simSeed}
	} else {
		n := *simSeeds
		if n <= 0 {
			n = 1
		}
		seeds = make([]int64, n)
		for i := range n {
			seeds[i] = int64(i)
		}
	}

	steps := *simSteps
	if steps <= 0 {
		steps = 120
	}

	start := time.Now()
	var totalStoreFaults, totalRuns, totalOps, totalAutos int64
	var totalSteps, totalCrashes, totalRepairs, totalUserWakes int

	for _, seed := range seeds {
		cfg := configForSeed(seed)
		w := sim.NewWorld(seed, cfg)
		if err := w.RunSimulation(ctx, steps); err != nil {
			if *simVerbose {
				t.Logf("\n%s\n--- full trace ---\n%s", w.DebugDump(ctx), w.Trace.FormatLast(0))
			}
			t.Fatalf(
				"coordination simulation failed:\n%v\n\nRepro:\n  go test ./internal/sim -run TestCoordinationSim -sim-seed=%d -sim-steps=%d -sim-verbose",
				err, seed, steps,
			)
		}

		storeStats := w.Faults.Stats()
		checkStats := w.Checker.Stats()

		totalStoreFaults += storeStats.Total()
		totalRuns += int64(checkStats.TrackedRuns)
		totalOps += int64(checkStats.TrackedOperations)
		totalAutos += int64(checkStats.TrackedAutomations)
		totalSteps += w.Stats.WorkerSteps
		totalCrashes += w.Stats.WorkerCrashes
		totalRepairs += w.Stats.RepairPasses
		totalUserWakes += w.Stats.UserWakesClaimed

		if *simVerbose {
			t.Logf("seed=%d users=%d digest=%s runs=%d ops=%d autos=%d workerSteps=%d crashes=%d repairs=%d userWakes=%d storeFaults=%d\n%s",
				seed, len(w.Users), w.Digest()[:16],
				checkStats.TrackedRuns, checkStats.TrackedOperations, checkStats.TrackedAutomations,
				w.Stats.WorkerSteps, w.Stats.WorkerCrashes, w.Stats.RepairPasses, w.Stats.UserWakesClaimed, storeStats.Total(),
				w.Trace.FormatLast(40),
			)
		}
	}

	elapsed := time.Since(start)
	t.Logf("completed %d seed(s) x %d steps in %s (runs=%d ops=%d autos=%d workerSteps=%d crashes=%d repairs=%d userWakes=%d storeFaults=%d)",
		len(seeds), steps, elapsed.Round(time.Millisecond),
		totalRuns, totalOps, totalAutos, totalSteps, totalCrashes, totalRepairs, totalUserWakes, totalStoreFaults,
	)

	// Guard against accidentally running a degenerate no-op workload across batch runs.
	if len(seeds) >= 8 {
		if totalRuns == 0 || totalOps == 0 || totalAutos == 0 || totalSteps == 0 {
			t.Fatalf("workload coverage too low: runs=%d ops=%d autos=%d workerSteps=%d", totalRuns, totalOps, totalAutos, totalSteps)
		}
		if totalStoreFaults == 0 || totalCrashes == 0 {
			t.Fatalf("fault/crash coverage did not trigger: storeFaults=%d crashes=%d", totalStoreFaults, totalCrashes)
		}
	}
}

func TestCoordinationSimDeterminism(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	seeds := []int64{0, 1, 7, 42, 1337, 9999}
	if *simSeed >= 0 {
		seeds = []int64{*simSeed}
	}
	steps := *simSteps
	if steps <= 0 {
		steps = 120
	}

	for _, seed := range seeds {
		cfg := configForSeed(seed)

		w1 := sim.NewWorld(seed, cfg)
		if err := w1.RunSimulation(ctx, steps); err != nil {
			t.Fatalf("seed=%d run1 failed: %v", seed, err)
		}
		d1 := w1.Digest()

		w2 := sim.NewWorld(seed, cfg)
		if err := w2.RunSimulation(ctx, steps); err != nil {
			t.Fatalf("seed=%d run2 failed: %v", seed, err)
		}
		d2 := w2.Digest()

		if d1 != d2 {
			t.Errorf("seed=%d non-deterministic world digest:\n  run1=%s\n  run2=%s", seed, d1, d2)
			if w1.Backend.Digest() != w2.Backend.Digest() {
				t.Errorf("  memstore.Backend diverged: %s vs %s", w1.Backend.Digest(), w2.Backend.Digest())
			}
			for i := range w1.Users {
				if w1.Users[i].Artifacts.Digest() != w2.Users[i].Artifacts.Digest() {
					t.Errorf("  user[%d] Artifacts diverged: %s vs %s", i, w1.Users[i].Artifacts.Digest(), w2.Users[i].Artifacts.Digest())
				}
			}
			if w1.Trace.Digest() != w2.Trace.Digest() {
				t.Errorf("  Trace diverged: %s vs %s\n--- run1 trace ---\n%s\n--- run2 trace ---\n%s",
					w1.Trace.Digest(), w2.Trace.Digest(), w1.Trace.FormatLast(20), w2.Trace.FormatLast(20))
			}
			t.FailNow()
		}
		t.Logf("seed=%d deterministic digest=%s", seed, d1)
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}
