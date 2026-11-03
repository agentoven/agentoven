package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type recordedBake struct{ kitchen, agent, version, env, by string }

type fakeBakeRecorder struct {
	mu   sync.Mutex
	got  []recordedBake
	fail bool
}

func (f *fakeBakeRecorder) RecordBakeDeployment(_ context.Context, kitchen, agent, version, env, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, recordedBake{kitchen, agent, version, env, by})
	if f.fail {
		return errors.New("the store is down")
	}
	return nil
}

func (f *fakeBakeRecorder) calls() []recordedBake {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedBake(nil), f.got...)
}

func waitCalls(t *testing.T, f *fakeBakeRecorder, n int) []recordedBake {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c := f.calls(); len(c) >= n {
			return c
		}
		time.Sleep(50 * time.Millisecond)
	}
	return f.calls()
}

// Owner decision A7: a bake records the deployment of the environment it targeted, once the agent is ready
// (not when the bake is asked for, so a burnt agent is not shown as deployed), and a recorder that fails
// does not undo the bake.
func TestABakeRecordsItsDeploymentOnTheTargetEnvironment(t *testing.T) {
	h, s := bakeHandlers(t)
	rec := &fakeBakeRecorder{fail: true}
	h.BakeDeployments = rec
	for i, slug := range []string{"dev", "prod"} {
		if err := s.CreateEnvironment(context.Background(), &models.Environment{ID: slug, Name: slug, Slug: slug, Kitchen: "default", Order: i}); err != nil {
			t.Fatal(err)
		}
	}

	if code, body := agentCall(t, h.BakeAgent, "POST", `{}`); code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", code, body)
	}
	baked := waitStatus(t, s, models.AgentStatusReady)
	calls := waitCalls(t, rec, 1)
	if len(calls) != 1 || calls[0].env != "dev" || calls[0].agent != "bot" || calls[0].version != baked.Version || calls[0].kitchen != "default" {
		t.Fatalf("a bake without an environment records the kitchen's first one: %+v (agent %s)", calls, baked.Version)
	}
	if baked.Status != models.AgentStatusReady {
		t.Fatal("a failing recorder must not undo the bake")
	}

	// A named environment is the one recorded; a re-cook records nothing (it does not target an environment).
	agentCall(t, h.UpdateAgent, "PUT", `{"description":"two"}`)
	if code, body := agentCall(t, h.BakeAgent, "POST", `{"environment":"prod"}`); code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", code, body)
	}
	calls = waitCalls(t, rec, 2)
	if len(calls) != 2 || calls[1].env != "prod" {
		t.Fatalf("%+v", calls)
	}
	agentCall(t, h.UpdateAgent, "PUT", `{"description":"three"}`)
	agentCall(t, h.RecookAgent, "POST", `{}`)
	waitStatus(t, s, models.AgentStatusReady)
	time.Sleep(1500 * time.Millisecond)
	if n := len(rec.calls()); n != 2 {
		t.Fatalf("a re-cook must not record a deployment: %d calls", n)
	}
}

// A kitchen with no environments has nowhere to record: the recorder is not called.
func TestABakeInAKitchenWithoutEnvironmentsRecordsNothing(t *testing.T) {
	h, s := bakeHandlers(t)
	rec := &fakeBakeRecorder{}
	h.BakeDeployments = rec
	agentCall(t, h.BakeAgent, "POST", `{}`)
	waitStatus(t, s, models.AgentStatusReady)
	time.Sleep(1500 * time.Millisecond)
	if n := len(rec.calls()); n != 0 {
		t.Fatalf("%d calls", n)
	}
}
