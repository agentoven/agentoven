package process

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func startScript(t *testing.T, le *LocalExecutor, body string) (*models.Agent, *models.ProcessInfo) {
	t.Helper()
	if findPython() == "" {
		t.Skip("the local executor needs python3 on the PATH")
	}
	agent := &models.Agent{Name: "a", Kitchen: "k", Runtime: models.RuntimeCustom, Entrypoint: script(t, body)}
	info := &models.ProcessInfo{AgentName: "a", Kitchen: "k", Port: 1, Endpoint: healthy(t)}
	if err := le.Start(context.Background(), agent, info, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	return agent, info
}

// healthy serves /health, which Start polls after the process says it is ready.
func healthy(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestStoppingAProcessReapsItExactlyOnce(t *testing.T) {
	le := NewLocalExecutor()
	_, info := startScript(t, le, "echo AGENT_READY\nexec sleep 60")
	pid := info.PID
	if !alive(pid) {
		t.Fatal("the process should be running")
	}
	// Under -race this fails if Stop and the reaper both call cmd.Wait.
	if err := le.Stop(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatal("the process was not stopped and reaped")
	}
	if err := le.Stop(context.Background(), info); err != nil {
		t.Fatalf("stopping a stopped process is not an error: %v", err)
	}
}

func TestAStaleExitDoesNotForgetAReplacementProcess(t *testing.T) {
	le := NewLocalExecutor()
	agent, info := startScript(t, le, "echo AGENT_READY\nexec sleep 60")
	old := info.PID
	if err := le.Stop(context.Background(), info); err != nil {
		t.Fatal(err)
	}

	// Start again under the same key, then let the old process's exit be reported late.
	agent2 := &models.Agent{Name: "a", Kitchen: "k", Runtime: models.RuntimeCustom, Entrypoint: agent.Entrypoint}
	info2 := &models.ProcessInfo{AgentName: "a", Kitchen: "k", Port: 1, Endpoint: healthy(t)}
	if err := le.Start(context.Background(), agent2, info2, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	defer le.Stop(context.Background(), info2)
	time.Sleep(200 * time.Millisecond)
	if info2.PID == old {
		t.Fatal("expected a different process")
	}
	if le.GetLogBuffer("k", "a") == nil {
		t.Fatal("the replacement process must still be tracked after the old one's exit")
	}
}

func TestAProcessThatExitsOnItsOwnIsForgotten(t *testing.T) {
	le := NewLocalExecutor()
	_, info := startScript(t, le, "echo AGENT_READY\nexit 0")
	deadline := time.Now().Add(5 * time.Second)
	for le.GetLogBuffer("k", "a") != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if le.GetLogBuffer("k", "a") != nil {
		t.Fatal("an exited process should be dropped from the table")
	}
	_ = info
}

func TestManagerReportsAProcessThatDiedAsStopped(t *testing.T) {
	m := NewManager()
	_, info := startScript(t, m.local, "echo AGENT_READY\nsleep 30")
	info.Mode, info.Status = models.ExecModeLocal, models.ProcessRunning
	m.processes[processKey("k", "a")] = info

	got, _ := m.Status(context.Background(), "k", "a")
	if got.Status != models.ProcessRunning || len(m.ListRunning()) != 1 {
		t.Fatalf("a live process should be running, got %s", got.Status)
	}

	// Killed from outside, the way an OOM kill or a stray pkill would.
	if err := syscall.Kill(info.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ = m.Status(context.Background(), "k", "a"); got.Status != models.ProcessRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status != models.ProcessStopped {
		t.Fatalf("a dead process must not stay running, got %s", got.Status)
	}
	if n := len(m.ListRunning()); n != 0 {
		t.Fatalf("ListRunning should not include a dead process, got %d", n)
	}
}
