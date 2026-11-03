package handlers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Review finding 7: six overlapping re-cooks started six processes. The steps of one agent run one at a
// time and only the newest does anything.
func TestOverlappingBakeStepsOfOneAgentRunOnceAndNeverTogether(t *testing.T) {
	var b bakeSerializer
	const n = 6
	gens := make([]uint64, n)
	for i := range gens {
		gens[i] = b.begin("k/a") // six requests arrive before any step runs
	}
	var running, maxRunning, ran int32
	var wg sync.WaitGroup
	for _, g := range gens {
		wg.Add(1)
		go func(g uint64) {
			defer wg.Done()
			b.run("k/a", g, func(func() bool) {
				cur := atomic.AddInt32(&running, 1)
				for {
					m := atomic.LoadInt32(&maxRunning)
					if cur <= m || atomic.CompareAndSwapInt32(&maxRunning, m, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				atomic.AddInt32(&ran, 1)
			})
		}(g)
	}
	wg.Wait()
	if ran != 1 || maxRunning != 1 {
		t.Fatalf("ran %d steps, at most %d at once; want one step, run alone", ran, maxRunning)
	}
	if len(b.locks) != 0 || len(b.gens) != 0 {
		t.Fatalf("the serializer must not keep entries for idle agents: %d locks, %d generations", len(b.locks), len(b.gens))
	}
}

func TestBakeStepsOfDifferentAgentsAndSuccessiveRequestsAllRun(t *testing.T) {
	var b bakeSerializer
	var ran int32
	step := func(func() bool) { atomic.AddInt32(&ran, 1) }
	for _, agent := range []string{"k/a", "k/b"} {
		for i := 0; i < 3; i++ { // each request is followed by its step before the next arrives
			b.run(agent, b.begin(agent), step)
		}
	}
	if ran != 6 {
		t.Fatalf("ran %d steps, want 6", ran)
	}
}

// A step that is waiting when a newer request arrives is told, so it can stop before doing work.
func TestAStepLearnsItWasSupersededWhileItWaited(t *testing.T) {
	var b bakeSerializer
	g1 := b.begin("k/a")
	var sawStale bool
	b.run("k/a", g1, func(superseded func() bool) {
		b.begin("k/a") // a newer re-cook arrives while this step sleeps
		sawStale = superseded()
	})
	if !sawStale {
		t.Fatal("the step must be able to see that a newer request began")
	}
}
