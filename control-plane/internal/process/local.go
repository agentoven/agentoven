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
	"strings"
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
	logs   *LogBuffer
}

// LocalExecutor spawns agent processes as Python subprocesses.
type LocalExecutor struct {
	mu        sync.Mutex
	processes map[string]*localProcess // key: kitchen/agentName
	scriptDir string                   // temp dir for extracted scripts
	repoDir   string                   // base dir for git-cloned agent repos
}

// NewLocalExecutor creates a new local executor.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{
		processes: make(map[string]*localProcess),
	}
}

// Start launches a Python process for the agent.
// For framework-native agents with a RepoURL, the repo is cloned/pulled first.
// If agent.Entrypoint is set, it is used instead of the embedded agent_runner.py.
func (le *LocalExecutor) Start(ctx context.Context, agent *models.Agent, info *models.ProcessInfo, env map[string]string) error {
	// ── Git pull for supported framework runtimes ─────────
	var workDir string
	if agent.NeedsGitPull() {
		dir, err := le.ensureRepo(ctx, agent)
		if err != nil {
			return fmt.Errorf("git sync failed for %s: %w", agent.RepoURL, err)
		}
		workDir = dir
		log.Info().Str("agent", agent.Name).Str("repo", agent.RepoURL).Str("dir", dir).Msg("Repo ready")
	}

	// Find Python executable
	pythonBin := findPython()
	if pythonBin == "" {
		return fmt.Errorf("python3 not found in PATH — install Python 3.10+ to use local execution mode")
	}

	// ── Select command: entrypoint override or embedded runner ──
	var cmd *exec.Cmd
	procCtx, cancel := context.WithCancel(context.Background())

	if agent.Entrypoint != "" {
		parts := strings.Fields(agent.Entrypoint)
		if len(parts) == 0 {
			cancel()
			return fmt.Errorf("agent entrypoint is empty after parsing")
		}
		cmd = exec.CommandContext(procCtx, parts[0], parts[1:]...)
	} else {
		// Default: embedded agent_runner.py
		scriptPath, err := le.ensureScript()
		if err != nil {
			cancel()
			return fmt.Errorf("failed to extract agent runner script: %w", err)
		}
		cmd = exec.CommandContext(procCtx, pythonBin, scriptPath)
	}

	// Set working directory to cloned repo (if applicable)
	if workDir != "" {
		cmd.Dir = workDir
	}

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
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Create log buffer for this agent (keep last 2000 lines)
	logBuf := NewLogBuffer(2000)

	// Start the process
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start agent process: %w", err)
	}

	info.PID = cmd.Process.Pid

	key := processKey(agent.Kitchen, agent.Name)
	le.mu.Lock()
	le.processes[key] = &localProcess{
		cmd:    cmd,
		cancel: cancel,
		pid:    cmd.Process.Pid,
		logs:   logBuf,
	}
	le.mu.Unlock()

	// Stream stderr to log buffer in the background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			logBuf.Write("stderr", line)
			log.Debug().Str("agent", agent.Name).Str("stream", "stderr").Msg(line)
		}
	}()

	log.Info().
		Str("agent", agent.Name).
		Int("pid", cmd.Process.Pid).
		Int("port", info.Port).
		Msg("Agent process started")

	// Wait for the "AGENT_READY" signal from stdout (with timeout)
	readyCh := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "AGENT_READY" {
				readyCh <- true
				// Continue reading stdout into log buffer
				for scanner.Scan() {
					logBuf.Write("stdout", scanner.Text())
				}
				return
			}
			logBuf.Write("stdout", line)
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

// ensureRepo clones or pulls the agent's git repo for supported framework runtimes.
// Returns the path to the cloned repo directory.
func (le *LocalExecutor) ensureRepo(ctx context.Context, agent *models.Agent) (string, error) {
	le.mu.Lock()
	baseDir := le.repoDir
	le.mu.Unlock()

	if baseDir == "" {
		dir, err := os.MkdirTemp("", "agentoven-repos-")
		if err != nil {
			return "", fmt.Errorf("failed to create repo base dir: %w", err)
		}
		le.mu.Lock()
		le.repoDir = dir
		baseDir = dir
		le.mu.Unlock()
	}

	// Stable per-agent directory derived from kitchen + name
	repoDir := filepath.Join(baseDir, agent.Kitchen, agent.Name)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		// First bake — clone the repo
		log.Info().Str("agent", agent.Name).Str("url", agent.RepoURL).Msg("Cloning agent repo")
		args := []string{"clone", agent.RepoURL, repoDir}
		if agent.RepoBranch != "" {
			args = []string{"clone", "--branch", agent.RepoBranch, "--single-branch", agent.RepoURL, repoDir}
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git clone failed: %s: %w", strings.TrimSpace(string(out)), err)
		}
	} else {
		// Subsequent bakes — pull latest
		log.Info().Str("agent", agent.Name).Str("dir", repoDir).Msg("Pulling latest agent repo")
		args := []string{"pull", "--ff-only"}
		if agent.RepoBranch != "" {
			args = []string{"pull", "--ff-only", "origin", agent.RepoBranch}
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Log but don't fail — stale repo is better than no repo
			log.Warn().Str("agent", agent.Name).Str("output", strings.TrimSpace(string(out))).Msg("git pull failed, proceeding with existing checkout")
		}
	}

	return repoDir, nil
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

// GetLogBuffer returns the log buffer for a running agent process, or nil if not found.
func (le *LocalExecutor) GetLogBuffer(kitchen, agentName string) *LogBuffer {
	key := processKey(kitchen, agentName)
	le.mu.Lock()
	defer le.mu.Unlock()
	proc, ok := le.processes[key]
	if !ok {
		return nil
	}
	return proc.logs
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
