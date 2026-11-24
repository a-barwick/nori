package sim_test

import (
	"context"
	"flag"
	"testing"
	"time"

	"nori/internal/sim"
	"nori/internal/sim/inner"
)

func flagExplicitlySet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func fullConfigForSeed(_ int64) sim.FullWorldConfig {
	return sim.FullWorldConfig{}
}

func TestFullStackSim(t *testing.T) {
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
		// Keep default `go test ./...` fast while honoring explicit `-sim-seeds=N`.
		if !flagExplicitlySet("sim-seeds") && n > 24 {
			n = 24
		}
		seeds = make([]int64, n)
		for i := range n {
			seeds[i] = int64(i)
		}
	}

	steps := *simSteps
	if steps <= 0 {
		steps = 100
	}
	if !flagExplicitlySet("sim-steps") && steps > 100 {
		steps = 100
	}

	start := time.Now()
	var (
		totalStoreFaults, totalRuns, totalAutos                 int64
		totalOuterInputs, totalOuterCalls, totalTakeovers       int
		totalInnerSteps, totalInnerModels, totalInnerWrites     int
		totalInteractions, totalDrainInteractions, totalCrashes int
		totalPauseToggles, totalRepairs, totalUserWakes         int
	)

	for _, seed := range seeds {
		cfg := fullConfigForSeed(seed)
		w := sim.NewFullWorld(seed, cfg)
		if err := w.RunFullSimulation(ctx, steps); err != nil {
			if *simVerbose {
				t.Logf("\n%s\n--- full trace ---\n%s", w.DebugDump(ctx), w.Trace.FormatLast(0))
			}
			t.Fatalf(
				"full-stack simulation failed:\n%v\n\nRepro:\n  go test -v ./internal/sim -run TestFullStackSim -sim-seed=%d -sim-steps=%d -sim-verbose",
				err, seed, steps,
			)
		}

		storeStats := w.Faults.Stats()
		checkStats := w.Checker.Stats()

		totalStoreFaults += storeStats.Total()
		totalRuns += int64(checkStats.TrackedRuns)
		totalAutos += int64(checkStats.TrackedAutomations)
		totalOuterInputs += w.Stats.OuterInputs
		totalOuterCalls += w.Stats.OuterProcessCalls
		totalTakeovers += w.Stats.OuterLeaseTakeover
		totalInnerSteps += w.Stats.InnerSteps
		totalInnerModels += len(w.Inner.Model.ModelCalls())
		totalInteractions += w.Stats.InteractionReplies
		totalDrainInteractions += w.Stats.DrainInteractions
		totalCrashes += w.Stats.InnerCrashes
		totalPauseToggles += w.Stats.PauseToggles
		totalRepairs += w.Stats.RepairPasses
		totalUserWakes += w.Stats.UserWakesClaimed

		writes := 0
		for _, ex := range w.Inner.Adapter.Executed() {
			if ex.ToolName == inner.ToolNameWrite || ex.ToolName == inner.ToolNameSend {
				writes++
			}
		}
		totalInnerWrites += writes

		if *simVerbose {
			t.Logf("seed=%d users=%d digest=%s runs=%d autos=%d outerCalls=%d innerSteps=%d innerModels=%d writes=%d interactions=%d crashes=%d repairs=%d userWakes=%d storeFaults=%d",
				seed, len(w.Users), w.Digest()[:16],
				checkStats.TrackedRuns, checkStats.TrackedAutomations,
				w.Stats.OuterProcessCalls, w.Stats.InnerSteps, len(w.Inner.Model.ModelCalls()), writes,
				w.Stats.InteractionReplies, w.Stats.InnerCrashes, w.Stats.RepairPasses, w.Stats.UserWakesClaimed, storeStats.Total(),
			)
		}
	}

	elapsed := time.Since(start)
	t.Logf("completed %d full-stack seed(s) x %d steps in %s (runs=%d autos=%d outerIn=%d outerCalls=%d takeovers=%d innerSteps=%d innerModels=%d writes=%d interactions=%d drainInteractions=%d crashes=%d pauses=%d repairs=%d userWakes=%d storeFaults=%d)",
		len(seeds), steps, elapsed.Round(time.Millisecond),
		totalRuns, totalAutos, totalOuterInputs, totalOuterCalls, totalTakeovers,
		totalInnerSteps, totalInnerModels, totalInnerWrites, totalInteractions, totalDrainInteractions, totalCrashes, totalPauseToggles,
		totalRepairs, totalUserWakes, totalStoreFaults,
	)

	// Guard against accidentally running a degenerate workload across batch runs.
	if len(seeds) >= 8 {
		if totalRuns == 0 || totalOuterCalls == 0 || totalInnerModels == 0 || totalAutos == 0 {
			t.Fatalf("full-stack coverage too low: runs=%d outerCalls=%d innerModels=%d autos=%d",
				totalRuns, totalOuterCalls, totalInnerModels, totalAutos)
		}
		if totalStoreFaults == 0 {
			t.Fatalf("fault injection did not trigger: storeFaults=%d", totalStoreFaults)
		}
	}
}

func TestFullStackDeterminism(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	seeds := []int64{0, 1, 7, 42}
	if *simSeed >= 0 {
		seeds = []int64{*simSeed}
	}
	steps := *simSteps
	if steps <= 0 {
		steps = 100
	}
	if !flagExplicitlySet("sim-steps") && steps > 100 {
		steps = 100
	}

	for _, seed := range seeds {
		cfg := fullConfigForSeed(seed)

		w1 := sim.NewFullWorld(seed, cfg)
		if err := w1.RunFullSimulation(ctx, steps); err != nil {
			t.Fatalf("seed=%d run1 failed: %v\n\nRepro:\n  go test -v ./internal/sim -run TestFullStackSim -sim-seed=%d -sim-steps=%d -sim-verbose",
				seed, err, seed, steps)
		}
		d1 := w1.Digest()

		w2 := sim.NewFullWorld(seed, cfg)
		if err := w2.RunFullSimulation(ctx, steps); err != nil {
			t.Fatalf("seed=%d run2 failed: %v\n\nRepro:\n  go test -v ./internal/sim -run TestFullStackSim -sim-seed=%d -sim-steps=%d -sim-verbose",
				seed, err, seed, steps)
		}
		d2 := w2.Digest()

		if d1 != d2 {
			t.Errorf("seed=%d non-deterministic full-stack digest:\n  run1=%s\n  run2=%s", seed, d1, d2)
			if w1.Backend.Digest() != w2.Backend.Digest() {
				t.Errorf("  memstore.Backend diverged: %s vs %s", w1.Backend.Digest(), w2.Backend.Digest())
			}
			for i := range w1.Users {
				if w1.Users[i].Artifacts.Digest() != w2.Users[i].Artifacts.Digest() {
					t.Errorf("  user[%d] Artifacts diverged: %s vs %s", i, w1.Users[i].Artifacts.Digest(), w2.Users[i].Artifacts.Digest())
				}
			}
			if w1.Inner.Adapter.Digest() != w2.Inner.Adapter.Digest() {
				t.Errorf("  Inner.Adapter diverged: %s vs %s", w1.Inner.Adapter.Digest(), w2.Inner.Adapter.Digest())
			}
			if w1.Inner.Model.Digest() != w2.Inner.Model.Digest() {
				t.Errorf("  Inner.Model diverged: %s vs %s", w1.Inner.Model.Digest(), w2.Inner.Model.Digest())
			}
			if !w1.Clock.Now().Equal(w2.Clock.Now()) {
				t.Errorf("  Clock diverged: %s vs %s", w1.Clock.Now(), w2.Clock.Now())
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
