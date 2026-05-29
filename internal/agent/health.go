package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type healthReport struct {
	InstanceID   string `json:"instance_id"`
	NodeID       string `json:"node_id"`
	HealthStatus string `json:"health_status"`
	Message      string `json:"message,omitempty"`
	CheckedAt    string `json:"checked_at"`
}

func (a *Agent) StartHealthChecker(ctx context.Context) {
	time.Sleep(30 * time.Second)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkAllPods(ctx)
		}
	}
}

func (a *Agent) checkAllPods(ctx context.Context) {
	runningPods := make(map[string]string)

	pods, err := listAdmiralPods(ctx)
	if err != nil {
		return
	}

	for _, pod := range pods {
		instanceID := extractInstanceID(pod.Name)
		if instanceID == "" {
			continue
		}
		runningPods[instanceID] = pod.Status

		status := "healthy"
		msg := ""
		if pod.Status != "Running" {
			status = "stopped"
			msg = fmt.Sprintf("pod status is %s", pod.Status)
		}

		report := healthReport{
			InstanceID:   instanceID,
			NodeID:       a.NodeID,
			HealthStatus: status,
			Message:      msg,
			CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		}

		if err := a.postHealth(report); err != nil {
			_ = err
		}
	}

	quadletPods := listQuadletPodFiles()
	for _, instanceID := range quadletPods {
		if _, ok := runningPods[instanceID]; !ok {
			report := healthReport{
				InstanceID:   instanceID,
				NodeID:       a.NodeID,
				HealthStatus: "stopped",
				Message:      "pod not running (Quadlet file exists but no pod found)",
				CheckedAt:    time.Now().UTC().Format(time.RFC3339),
			}
			if err := a.postHealth(report); err != nil {
				_ = err
			}
		}
	}
}

func listQuadletPodFiles() []string {
	matches, err := filepath.Glob("/etc/containers/systemd/admiral/*.pod")
	if err != nil {
		return nil
	}
	var ids []string
	for _, m := range matches {
		name := filepath.Base(m)
		name = strings.TrimSuffix(name, ".pod")
		id := extractInstanceID(name)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

type podInfo struct {
	Name   string
	Status string
}

func listAdmiralPods(ctx context.Context) ([]podInfo, error) {
	cmd := exec.CommandContext(ctx, "podman", "pod", "ps", "--format", "{{.Name}}\t{{.Status}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	var pods []podInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		status := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(name, "admiral-") {
			continue
		}
		pods = append(pods, podInfo{Name: name, Status: status})
	}
	return pods, nil
}

func extractInstanceID(podName string) string {
	if !strings.HasPrefix(podName, "admiral-") {
		return ""
	}
	return strings.TrimPrefix(podName, "admiral-")
}

func (a *Agent) postHealth(report healthReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode health report: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, a.APIURL+"/api/v1/fleet/health", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admiral-Token", a.SharedToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("send health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health callback failed with HTTP %d", resp.StatusCode)
	}
	return nil
}
