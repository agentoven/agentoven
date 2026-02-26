package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// k8sResource tracks a deployed K8s resource.
type k8sResource struct {
	podName   string
	agentName string
	kitchen   string
	namespace string
}

// K8sExecutor manages agent processes as Kubernetes Deployments.
type K8sExecutor struct {
	mu        sync.Mutex
	resources map[string]*k8sResource // key: kitchen/agentName
	namespace string
	image     string
}

// NewK8sExecutor creates a new Kubernetes executor.
func NewK8sExecutor() *K8sExecutor {
	return &K8sExecutor{
		resources: make(map[string]*k8sResource),
		namespace: "agentoven",
		image:     DefaultAgentImage,
	}
}

// Start deploys an agent as a Kubernetes Deployment + Service.
func (ke *K8sExecutor) Start(ctx context.Context, agent *models.Agent, info *models.ProcessInfo, env map[string]string) error {
	// Check if kubectl is available
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not found in PATH — install kubectl to use k8s execution mode")
	}

	deployName := fmt.Sprintf("agent-%s-%s", agent.Kitchen, agent.Name)
	serviceName := deployName + "-svc"
	namespace := ke.namespace

	// Use custom image if specified
	image := ke.image
	if agent.Tags != nil {
		if customImage, ok := agent.Tags["docker_image"]; ok && customImage != "" {
			image = customImage
		}
	}

	// Always use port 9000 inside the container
	env["AGENT_PORT"] = "9000"

	// Build K8s manifest
	manifest := ke.buildManifest(deployName, serviceName, namespace, image, info.Port, env)

	log.Info().
		Str("agent", agent.Name).
		Str("deployment", deployName).
		Str("namespace", namespace).
		Int("port", info.Port).
		Msg("Deploying agent to Kubernetes")

	// Apply the manifest
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(manifest)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply failed: %s: %w", stderr.String(), err)
	}

	// Wait for the pod to be ready
	podName, err := ke.waitForPod(ctx, deployName, namespace, 60*time.Second)
	if err != nil {
		log.Warn().Err(err).Str("agent", agent.Name).Msg("K8s pod readiness check failed")
	}

	info.PodName = podName
	info.Endpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local:9000", serviceName, namespace)

	key := processKey(agent.Kitchen, agent.Name)
	ke.mu.Lock()
	ke.resources[key] = &k8sResource{
		podName:   podName,
		agentName: agent.Name,
		kitchen:   agent.Kitchen,
		namespace: namespace,
	}
	ke.mu.Unlock()

	log.Info().
		Str("agent", agent.Name).
		Str("pod", podName).
		Str("service", serviceName).
		Msg("Agent deployed to Kubernetes")

	return nil
}

// Stop deletes the K8s Deployment and Service for an agent.
func (ke *K8sExecutor) Stop(_ context.Context, info *models.ProcessInfo) error {
	key := processKey(info.Kitchen, info.AgentName)

	ke.mu.Lock()
	res, ok := ke.resources[key]
	namespace := ke.namespace
	if ok {
		namespace = res.namespace
		delete(ke.resources, key)
	}
	ke.mu.Unlock()

	deployName := fmt.Sprintf("agent-%s-%s", info.Kitchen, info.AgentName)
	serviceName := deployName + "-svc"

	log.Info().
		Str("agent", info.AgentName).
		Str("deployment", deployName).
		Msg("Deleting K8s agent deployment")

	// Delete deployment
	cmd := exec.Command("kubectl", "delete", "deployment", deployName, "-n", namespace, "--ignore-not-found")
	_ = cmd.Run()

	// Delete service
	svcCmd := exec.Command("kubectl", "delete", "service", serviceName, "-n", namespace, "--ignore-not-found")
	_ = svcCmd.Run()

	return nil
}

// buildManifest generates a K8s YAML manifest with Deployment + Service.
func (ke *K8sExecutor) buildManifest(deployName, serviceName, namespace, image string, nodePort int, env map[string]string) string {
	// Build env var YAML
	var envYAML strings.Builder
	for k, v := range env {
		// Escape YAML values
		escaped := strings.ReplaceAll(v, "\"", "\\\"")
		fmt.Fprintf(&envYAML, "        - name: %s\n          value: \"%s\"\n", k, escaped)
	}

	return fmt.Sprintf(`---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    agentoven.dev/component: agent
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
        agentoven.dev/component: agent
    spec:
      containers:
      - name: agent
        image: %s
        ports:
        - containerPort: 9000
        env:
%s
        readinessProbe:
          httpGet:
            path: /health
            port: 9000
          initialDelaySeconds: 5
          periodSeconds: 10
        livenessProbe:
          httpGet:
            path: /health
            port: 9000
          initialDelaySeconds: 10
          periodSeconds: 30
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    agentoven.dev/component: agent
spec:
  selector:
    app: %s
  ports:
  - port: 9000
    targetPort: 9000
    protocol: TCP
  type: ClusterIP
`,
		deployName, namespace, deployName,
		deployName, deployName, image,
		envYAML.String(),
		serviceName, namespace, deployName, deployName,
	)
}

// waitForPod waits for the deployment's pod to be ready.
func (ke *K8sExecutor) waitForPod(ctx context.Context, deployName, namespace string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Get pod name from deployment
		cmd := exec.CommandContext(ctx, "kubectl", "get", "pods",
			"-n", namespace,
			"-l", fmt.Sprintf("app=%s", deployName),
			"-o", "jsonpath={.items[0].metadata.name}",
		)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err == nil {
			podName := strings.TrimSpace(stdout.String())
			if podName != "" {
				// Check pod status
				statusCmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName,
					"-n", namespace, "-o", "jsonpath={.status.phase}",
				)
				var statusOut bytes.Buffer
				statusCmd.Stdout = &statusOut
				if err := statusCmd.Run(); err == nil {
					if strings.TrimSpace(statusOut.String()) == "Running" {
						return podName, nil
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("pod not ready after %s", timeout)
}

// waitForHealth polls the health endpoint (used when running locally, not in-cluster).
func (ke *K8sExecutor) waitForHealth(endpoint string, timeout time.Duration) error {
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
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("k8s agent health check timed out after %s", timeout)
}

// unused but kept for future pod-status queries
var _ = json.Marshal
