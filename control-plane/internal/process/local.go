package process

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

//go:embed templates/agent_runner.py
var agentRunnerFS embed.FS

// localProcess tracks a running Python subprocess.
type localProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	pid    int
}

// LocalExecutor spawns agent processes as Python subprocesses.
type LocalExecutor struct {
	mu        sync.Mutex
	processes map[string]*localProcess // key: kitchen/agentName
	scriptDir string                   // temp dir for extracted scripts
}

// NewLocalExecutor creates a new local executor.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{
		processes: make(map[string]*localProcess),
	}
}

// Start launches a Python process running the embedded agent_runner.py template.
func (le *LocalExecutor) Start(ctx context.Context, agent *models.Agent, info *models.ProcessInfo, env map[string]string) error {
	// Extract the embedded agent runner script to a temp directory
	scriptPath, err := le.ensureScript()
	if err != nil {
		return fmt.Errorf("failed to extract agent runner script: %w", err)
	}

	// Find Python executable
	pythonBin := findPython()
	if pythonBin == "" {
		return fmt.Errorf("python3 not found in PATH — install Python 3.10+ to use local execution mode")
	}

	// Create a cancellable context for the process
	procCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(procCtx, pythonBin, scriptPath)

	// Build environment: inherit parent env + add agent-specific vars
	cmdEnv := os.Environ()
	for k, v := range env {
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = cmdEnv

	// Capture stdout for ready signal, stderr for logs
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // agent logs go to control plane stderr

	// Start the process
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start python process: %w", err)
	}

	info.PID = cmd.Process.Pid

	key := processKey(agent.Kitchen, agent.Name)
	le.mu.Lock()
	le.processes[key] = &localProcess{
		cmd:    cmd,
		cancel: cancel,
		pid:    cmd.Process.Pid,
	}
	le.mu.Unlock()

	log.Info().
		Str("agent", agent.Name).
		Int("pid", cmd.Process.Pid).
		Int("port", info.Port).
		Msg("Python agent process started")

	// Wait for the "AGENT_READY" signal from stdout (with timeout)
	readyCh := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "AGENT_READY" {
				readyCh <- true
				return
			}
		}
		readyCh <- false
	}()

	select {
	case ready := <-readyCh:
		if !ready {
			le.Stop(ctx, info)
			return fmt.Errorf("agent process exited before becoming ready")
		}
	case <-time.After(15 * time.Second):
		// Timeout waiting for ready signal — check if process is still alive
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			le.Stop(ctx, info)
			return fmt.Errorf("agent process exited during startup")
		}
		log.Warn().Str("agent", agent.Name).Msg("Agent process did not send ready signal within 15s, proceeding anyway")
	}

	// Verify the process is healthy via HTTP
	if err := le.waitForHealth(info.Endpoint, 10*time.Second); err != nil {
		log.Warn().Err(err).Str("agent", agent.Name).Msg("Agent health check failed, process may still be starting")
	}

	// Monitor the process in the background
	go func() {
		_ = cmd.Wait()
		le.mu.Lock()
		delete(le.processes, key)
		le.mu.Unlock()
		log.Info().Str("agent", agent.Name).Int("pid", info.PID).Msg("Agent process exited")
	}()

	return nil
}

// Stop kills a running Python process.
func (le *LocalExecutor) Stop(_ context.Context, info *models.ProcessInfo) error {
	key := processKey(info.Kitchen, info.AgentName)

	le.mu.Lock()
	proc, ok := le.processes[key]
	if !ok {
		le.mu.Unlock()
		return nil
	}
	delete(le.processes, key)
	le.mu.Unlock()

	log.Info().
		Str("agent", info.AgentName).
		Int("pid", proc.pid).
		Msg("Stopping local agent process")

	// Cancel the context (sends SIGKILL on timeout)
	proc.cancel()

	// Try graceful kill first
	if proc.cmd.Process != nil {
		_ = proc.cmd.Process.Signal(os.Interrupt) // SIGINT
		// Give it 3 seconds to shut down gracefully
		done := make(chan struct{})
		go func() {
			_ = proc.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Clean exit
		case <-time.After(3 * time.Second):
			// Force kill
			_ = proc.cmd.Process.Kill()
		}
	}

	return nil
}

// ensureScript extracts the embedded agent_runner.py to a temp directory.
func (le *LocalExecutor) ensureScript() (string, error) {
	le.mu.Lock()
	defer le.mu.Unlock()

	if le.scriptDir != "" {
		path := filepath.Join(le.scriptDir, "agent_runner.py")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	// Create temp directory
	dir, err := os.MkdirTemp("", "agentoven-runners-*")
	if err != nil {
		return "", err
	}
	le.scriptDir = dir

	// Read embedded script
	data, err := agentRunnerFS.ReadFile("templates/agent_runner.py")
	if err != nil {
		return "", fmt.Errorf("failed to read embedded agent runner: %w", err)
	}

	// Write to temp file
	path := filepath.Join(dir, "agent_runner.py")
	if err := os.WriteFile(path, data, 0755); err != nil {
		return "", err
	}

	return path, nil
}

// waitForHealth polls the agent's health endpoint until it responds.
func (le *LocalExecutor) waitForHealth(endpoint string, timeout time.Duration) error {
	healthURL := endpoint + "/health"
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	return fmt.Errorf("health check timed out after %s", timeout)
}

// findPython searches for a Python 3 executable.
func findPython() string {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}
