package process

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

const (
	k8sTokenPath  = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	k8sCAPath     = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	k8sNSPath     = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	k8sDefaultAPI = "https://kubernetes.default.svc"
)

// k8sResource tracks a deployed K8s resource.
type k8sResource struct {
	podName   string
	agentName string
	kitchen   string
	namespace string
}

// K8sExecutor manages agent processes as Kubernetes Deployments.
// Uses the in-cluster service account token to call the Kubernetes API server
// directly — no kubectl dependency.
type K8sExecutor struct {
	mu        sync.Mutex
	resources map[string]*k8sResource // key: kitchen/agentName
	namespace string
	image     string
}

// k8sClient holds HTTP client and auth info for the Kubernetes API.
type k8sClient struct {
	http      *http.Client
	apiServer string
	token     string
}

// NewK8sExecutor creates a new Kubernetes executor.
func NewK8sExecutor() *K8sExecutor {
	ns := "agentoven"
	if data, err := os.ReadFile(k8sNSPath); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			ns = s
		}
	}
	image := DefaultAgentImage
	if env := os.Getenv("AGENTOVEN_K8S_AGENT_IMAGE"); env != "" {
		image = env
	}
	return &K8sExecutor{
		resources: make(map[string]*k8sResource),
		namespace: ns,
		image:     image,
	}
}

// newK8sClient builds an authenticated HTTP client using the in-cluster service account.
// Supports KUBE_API_SERVER / KUBE_TOKEN / KUBE_INSECURE env var overrides.
func newK8sClient() (*k8sClient, error) {
	apiServer := k8sDefaultAPI
	if env := os.Getenv("KUBE_API_SERVER"); env != "" {
		apiServer = env
	}

	var tlsCfg *tls.Config
	if os.Getenv("KUBE_INSECURE") == "true" {
		tlsCfg = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — explicit opt-in
	} else {
		caBytes, err := os.ReadFile(k8sCAPath)
		if err != nil {
			return nil, fmt.Errorf("read k8s CA cert %s: %w", k8sCAPath, err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caBytes)
		tlsCfg = &tls.Config{RootCAs: pool}
	}

	token := os.Getenv("KUBE_TOKEN")
	if token == "" {
		tokenBytes, err := os.ReadFile(k8sTokenPath)
		if err != nil {
			return nil, fmt.Errorf("read k8s service account token: %w — ensure the pod has a service account with deployment/service RBAC", err)
		}
		token = strings.TrimSpace(string(tokenBytes))
	}

	return &k8sClient{
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   15 * time.Second,
		},
		apiServer: apiServer,
		token:     token,
	}, nil
}

// do executes an authenticated k8s API request. Pass nil body for GET/DELETE.
func (c *k8sClient) do(ctx context.Context, method, path string, body interface{}) (int, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal k8s request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.apiServer+path, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, err
}

// Start deploys an agent as a Kubernetes Deployment + Service via the k8s REST API.
func (ke *K8sExecutor) Start(ctx context.Context, agent *models.Agent, info *models.ProcessInfo, env map[string]string) error {
	client, err := newK8sClient()
	if err != nil {
		return fmt.Errorf("k8s client init failed: %w", err)
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

	env["AGENT_PORT"] = "9000"

	envList := make([]map[string]interface{}, 0, len(env))
	for k, v := range env {
		envList = append(envList, map[string]interface{}{"name": k, "value": v})
	}

	deployment := buildDeploymentManifest(deployName, namespace, image, envList)
	service := buildServiceManifest(deployName, serviceName, namespace)

	log.Info().
		Str("agent", agent.Name).
		Str("deployment", deployName).
		Str("namespace", namespace).
		Str("image", image).
		Msg("Deploying agent to Kubernetes via REST API")

	// Create Deployment; replace on conflict.
	deployPath := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments", namespace)
	status, _, err := client.do(ctx, http.MethodPost, deployPath, deployment)
	if err != nil {
		return fmt.Errorf("create k8s deployment: %w", err)
	}
	if status == http.StatusConflict {
		replaceStatus, _, replaceErr := client.do(ctx, http.MethodPut,
			fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", namespace, deployName), deployment)
		if replaceErr != nil {
			return fmt.Errorf("update k8s deployment: %w", replaceErr)
		}
		if replaceStatus >= 300 {
			return fmt.Errorf("update k8s deployment returned HTTP %d", replaceStatus)
		}
	} else if status >= 300 {
		return fmt.Errorf("create k8s deployment returned HTTP %d", status)
	}

	// Create Service; ignore conflict (already exists is fine).
	svcPath := fmt.Sprintf("/api/v1/namespaces/%s/services", namespace)
	svcStatus, _, err := client.do(ctx, http.MethodPost, svcPath, service)
	if err != nil {
		return fmt.Errorf("create k8s service: %w", err)
	}
	if svcStatus >= 300 && svcStatus != http.StatusConflict {
		return fmt.Errorf("create k8s service returned HTTP %d", svcStatus)
	}

	// Set endpoint before waiting so it is populated even if pod check times out.
	info.Endpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local:9000", serviceName, namespace)

	podName, err := ke.waitForPod(ctx, client, deployName, namespace, 60*time.Second)
	if err != nil {
		return fmt.Errorf("k8s pod did not reach running state: %w", err)
	}
	info.PodName = podName

	if err := ke.waitForHealth(info.Endpoint, 60*time.Second); err != nil {
		return fmt.Errorf("k8s agent health check failed: %w", err)
	}

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
		Str("endpoint", info.Endpoint).
		Msg("Agent deployed to Kubernetes")

	return nil
}

// Stop deletes the K8s Deployment and Service for an agent via the REST API.
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
		Msg("Deleting K8s agent deployment via REST API")

	client, err := newK8sClient()
	if err != nil {
		log.Warn().Err(err).Str("agent", info.AgentName).Msg("k8s client init failed during stop; resources may need manual cleanup")
		return nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	status, _, err := client.do(stopCtx, http.MethodDelete,
		fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", namespace, deployName), nil)
	if err != nil {
		log.Warn().Err(err).Str("deployment", deployName).Msg("delete k8s deployment failed")
	} else if status >= 300 && status != http.StatusNotFound {
		log.Warn().Int("status", status).Str("deployment", deployName).Msg("delete k8s deployment returned unexpected status")
	}

	svcStatus, _, err := client.do(stopCtx, http.MethodDelete,
		fmt.Sprintf("/api/v1/namespaces/%s/services/%s", namespace, serviceName), nil)
	if err != nil {
		log.Warn().Err(err).Str("service", serviceName).Msg("delete k8s service failed")
	} else if svcStatus >= 300 && svcStatus != http.StatusNotFound {
		log.Warn().Int("status", svcStatus).Str("service", serviceName).Msg("delete k8s service returned unexpected status")
	}

	return nil
}

// waitForPod polls the k8s API until a pod for the deployment reaches Running phase.
func (ke *K8sExecutor) waitForPod(ctx context.Context, client *k8sClient, deployName, namespace string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		podListPath := fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=app%%3D%s", namespace, deployName)
		status, body, err := client.do(ctx, http.MethodGet, podListPath, nil)
		if err != nil || status != http.StatusOK {
			time.Sleep(2 * time.Second)
			continue
		}

		var podList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal(body, &podList) != nil || len(podList.Items) == 0 {
			time.Sleep(2 * time.Second)
			continue
		}

		pod := podList.Items[0]
		if pod.Status.Phase == "Running" {
			return pod.Metadata.Name, nil
		}

		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("pod for deployment %s not Running after %s", deployName, timeout)
}

// waitForHealth polls the /health endpoint until it returns 200.
func (ke *K8sExecutor) waitForHealth(endpoint string, timeout time.Duration) error {
	healthURL := endpoint + "/health"
	deadline := time.Now().Add(timeout)
	hc := &http.Client{Timeout: 3 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := hc.Get(healthURL) // #nosec G107 — internal cluster DNS
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("k8s agent health check timed out after %s", timeout)
}

// RecentLogs fetches recent pod logs for a k8s-managed agent.
func (ke *K8sExecutor) RecentLogs(ctx context.Context, podName string, tailLines int) ([]LogEntry, error) {
	if podName == "" {
		return nil, fmt.Errorf("pod name is required")
	}
	if tailLines <= 0 {
		tailLines = 200
	}

	client, err := newK8sClient()
	if err != nil {
		return nil, fmt.Errorf("k8s client init failed: %w", err)
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?tailLines=%d", ke.namespace, podName, tailLines)
	status, body, err := client.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get pod logs failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get pod logs returned HTTP %d", status)
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return []LogEntry{}, nil
	}

	lines := strings.Split(text, "\n")
	now := time.Now().UTC()
	entries := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entries = append(entries, LogEntry{Timestamp: now, Stream: "stdout", Line: line})
	}

	return entries, nil
}

// StreamLogs follows pod logs and emits entries until the context is cancelled.
func (ke *K8sExecutor) StreamLogs(ctx context.Context, podName string, tailLines int) (<-chan LogEntry, error) {
	if podName == "" {
		return nil, fmt.Errorf("pod name is required")
	}
	if tailLines <= 0 {
		tailLines = 100
	}

	client, err := newK8sClient()
	if err != nil {
		return nil, fmt.Errorf("k8s client init failed: %w", err)
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?follow=true&tailLines=%d", ke.namespace, podName, tailLines)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.apiServer+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build pod log stream request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+client.token)

	resp, err := client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pod log stream request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pod log stream returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	out := make(chan LogEntry, 128)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			entry := LogEntry{Timestamp: time.Now().UTC(), Stream: "stdout", Line: line}
			select {
			case <-ctx.Done():
				return
			case out <- entry:
			}
		}
	}()

	return out, nil
}

// buildDeploymentManifest returns a Deployment map ready for JSON encoding.
func buildDeploymentManifest(deployName, namespace, image string, envList []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      deployName,
			"namespace": namespace,
			"labels":    map[string]string{"app": deployName, "agentoven.dev/component": "agent"},
		},
		"spec": map[string]interface{}{
			"replicas": 1,
			"selector": map[string]interface{}{
				"matchLabels": map[string]string{"app": deployName},
			},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]string{"app": deployName, "agentoven.dev/component": "agent"},
				},
				"spec": map[string]interface{}{
					"imagePullSecrets": []map[string]interface{}{
						{"name": "ghcr-credentials"},
					},
					"containers": []map[string]interface{}{{
						"name":            "agent",
						"image":           image,
						"imagePullPolicy": "Always",
						"ports":           []map[string]interface{}{{"containerPort": 9000}},
						"env":             envList,
						"readinessProbe": map[string]interface{}{
							"httpGet":             map[string]interface{}{"path": "/health", "port": 9000},
							"initialDelaySeconds": 5,
							"periodSeconds":       10,
						},
						"livenessProbe": map[string]interface{}{
							"httpGet":             map[string]interface{}{"path": "/health", "port": 9000},
							"initialDelaySeconds": 10,
							"periodSeconds":       30,
						},
						"resources": map[string]interface{}{
							"requests": map[string]string{"memory": "128Mi", "cpu": "100m"},
							"limits":   map[string]string{"memory": "512Mi", "cpu": "500m"},
						},
					}},
				},
			},
		},
	}
}

// buildServiceManifest returns a ClusterIP Service map ready for JSON encoding.
func buildServiceManifest(deployName, serviceName, namespace string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]interface{}{
			"name":      serviceName,
			"namespace": namespace,
			"labels":    map[string]string{"app": deployName, "agentoven.dev/component": "agent"},
		},
		"spec": map[string]interface{}{
			"selector": map[string]string{"app": deployName},
			"ports":    []map[string]interface{}{{"port": 9000, "targetPort": 9000, "protocol": "TCP"}},
			"type":     "ClusterIP",
		},
	}
}
