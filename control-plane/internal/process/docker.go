package process

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// DefaultAgentImage is the Docker image used for agent processes.
// Pro can override this with custom images per agent.
const DefaultAgentImage = "agentoven/agent-runner:latest"

// dockerContainer tracks a running Docker container.
type dockerContainer struct {
	containerID string
	agentName   string
	kitchen     string
}

// DockerExecutor manages agent processes as Docker containers.
type DockerExecutor struct {
	mu         sync.Mutex
	containers map[string]*dockerContainer // key: kitchen/agentName
	image      string
}

// NewDockerExecutor creates a new Docker executor.
func NewDockerExecutor() *DockerExecutor {
	return &DockerExecutor{
		containers: make(map[string]*dockerContainer),
		image:      DefaultAgentImage,
	}
}

// Start runs an agent as a Docker container.
func (de *DockerExecutor) Start(ctx context.Context, agent *models.Agent, info *models.ProcessInfo, env map[string]string) error {
	// Check if docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH — install Docker to use docker execution mode")
	}

	// Build docker run args
	containerName := fmt.Sprintf("agentoven-%s-%s", agent.Kitchen, agent.Name)

	args := []string{
		"run", "-d",
		"--name", containerName,
		"-p", fmt.Sprintf("%d:9000", info.Port), // map host port to container port 9000
	}

	// Always set AGENT_PORT to 9000 inside the container (mapped to host port)
	env["AGENT_PORT"] = "9000"

	// Add environment variables
	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Use custom image if specified in agent tags, otherwise default
	image := de.image
	if agent.Tags != nil {
		if customImage, ok := agent.Tags["docker_image"]; ok && customImage != "" {
			image = customImage
		}
	}

	args = append(args, image)

	log.Info().
		Str("agent", agent.Name).
		Str("container", containerName).
		Str("image", image).
		Int("port", info.Port).
		Msg("Starting Docker agent container")

	// Run docker command
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run failed: %s: %w", stderr.String(), err)
	}

	containerID := strings.TrimSpace(stdout.String())
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}

	info.ContainerID = containerID
	info.Endpoint = fmt.Sprintf("http://localhost:%d", info.Port)

	key := processKey(agent.Kitchen, agent.Name)
	de.mu.Lock()
	de.containers[key] = &dockerContainer{
		containerID: containerID,
		agentName:   agent.Name,
		kitchen:     agent.Kitchen,
	}
	de.mu.Unlock()

	// Wait for the container to be healthy
	if err := de.waitForHealth(info.Endpoint, 30*time.Second); err != nil {
		log.Warn().Err(err).Str("agent", agent.Name).Msg("Docker container health check failed")
	}

	log.Info().
		Str("agent", agent.Name).
		Str("container_id", containerID).
		Msg("Docker agent container started")

	return nil
}

// Stop removes a Docker container.
func (de *DockerExecutor) Stop(_ context.Context, info *models.ProcessInfo) error {
	key := processKey(info.Kitchen, info.AgentName)

	de.mu.Lock()
	container, ok := de.containers[key]
	if !ok {
		de.mu.Unlock()
		// Try to stop by container name as fallback
		containerName := fmt.Sprintf("agentoven-%s-%s", info.Kitchen, info.AgentName)
		return de.stopByName(containerName)
	}
	delete(de.containers, key)
	de.mu.Unlock()

	log.Info().
		Str("agent", info.AgentName).
		Str("container_id", container.containerID).
		Msg("Stopping Docker agent container")

	// Stop the container (5s graceful timeout)
	cmd := exec.Command("docker", "stop", "-t", "5", container.containerID)
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Str("container", container.containerID).Msg("Failed to stop container, forcing removal")
	}

	// Remove the container
	rmCmd := exec.Command("docker", "rm", "-f", container.containerID)
	_ = rmCmd.Run()

	return nil
}

// stopByName stops a container by its name (fallback when containerID is lost).
func (de *DockerExecutor) stopByName(name string) error {
	cmd := exec.Command("docker", "rm", "-f", name)
	return cmd.Run()
}

// waitForHealth polls the container's health endpoint.
func (de *DockerExecutor) waitForHealth(endpoint string, timeout time.Duration) error {
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
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("docker container health check timed out after %s", timeout)
}
