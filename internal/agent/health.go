package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
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

func (a *Agent) StartStorageChecker(ctx context.Context) {
	interval := 60 * time.Second
	if a.StorageCheckInterval != "" {
		if d, err := time.ParseDuration(a.StorageCheckInterval); err == nil && d > 0 {
			interval = d
		}
	}

	time.Sleep(30 * time.Second)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkInstanceStorage(ctx)
		}
	}
}

type instanceVolInfo struct {
	InstanceID   string
	VolumeName   string
	Mountpoint   string
	StorageLimit int64
}

func (a *Agent) checkInstanceStorage(ctx context.Context) {
	instances := listQuadletPodFiles()
	if len(instances) == 0 {
		return
	}

	for _, instanceID := range instances {
		report := a.measureInstanceStorage(ctx, instanceID)
		if report == nil {
			continue
		}
		if err := a.postStorage(*report); err != nil {
			_ = err
		}
	}
}

func (a *Agent) measureInstanceStorage(ctx context.Context, instanceID string) *admiral.StorageReport {
	now := time.Now().UTC().Format(time.RFC3339)

	limitBytes := int64(0)
	storageLimit := readInstanceStorageLimit(instanceID)
	if storageLimit != "" {
		limitBytes = parseStorageLimitBytes(storageLimit)
	}
	if limitBytes <= 0 {
		return &admiral.StorageReport{
			InstanceID:     instanceID,
			NodeID:         a.NodeID,
			StorageState:   admiral.StorageUnknown,
			StorageMessage: "no storage limit configured",
			CheckedAt:      now,
		}
	}

	usedBytes := int64(0)
	mountpoint := findVolumeMountpoint(ctx, instanceID)
	if mountpoint == "" {
		return &admiral.StorageReport{
			InstanceID:        instanceID,
			NodeID:            a.NodeID,
			StorageLimitBytes: limitBytes,
			StorageState:      admiral.StorageUnknown,
			StorageMessage:    "no volume mountpoint found",
			CheckedAt:         now,
		}
	}

	usedBytes = measureDirUsage(ctx, mountpoint)
	if usedBytes < 0 {
		return &admiral.StorageReport{
			InstanceID:        instanceID,
			NodeID:            a.NodeID,
			StorageLimitBytes: limitBytes,
			StorageState:      admiral.StorageUnknown,
			StorageMessage:    "failed to measure storage usage",
			CheckedAt:         now,
		}
	}

	usedPct := float64(0)
	if limitBytes > 0 {
		usedPct = math.Round(float64(usedBytes)/float64(limitBytes)*10000) / 100
	}

	state, msg := classifyStorageState(usedPct)

	return &admiral.StorageReport{
		InstanceID:        instanceID,
		NodeID:            a.NodeID,
		StorageLimitBytes: limitBytes,
		StorageUsedBytes:  usedBytes,
		StorageUsedPct:    usedPct,
		StorageState:      state,
		StorageMessage:    msg,
		CheckedAt:         now,
	}
}

func readInstanceStorageLimit(instanceID string) string {
	// Check for tier.json in instance data dir
	paths := []string{
		filepath.Join("/var/lib/admiral/instances", instanceID, "tier.json"),
		filepath.Join("/var/lib/admiral/instances", instanceID, "instance.json"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var meta struct {
			Storage string `json:"storage"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		return meta.Storage
	}
	return ""
}

func parseStorageLimitBytes(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	lower := strings.ToLower(value)
	multiplier := int64(1)

	switch {
	case strings.HasSuffix(lower, "tib"), strings.HasSuffix(lower, "ti"), strings.HasSuffix(lower, "tb"):
		multiplier = 1024 * 1024 * 1024 * 1024
		value = value[:len(value)-2]
	case strings.HasSuffix(lower, "t"):
		multiplier = 1024 * 1024 * 1024 * 1024
		value = value[:len(value)-1]
	case strings.HasSuffix(lower, "gib"), strings.HasSuffix(lower, "gi"), strings.HasSuffix(lower, "gb"):
		multiplier = 1024 * 1024 * 1024
		value = value[:len(value)-2]
	case strings.HasSuffix(lower, "g"):
		multiplier = 1024 * 1024 * 1024
		value = value[:len(value)-1]
	case strings.HasSuffix(lower, "mib"), strings.HasSuffix(lower, "mi"), strings.HasSuffix(lower, "mb"):
		multiplier = 1024 * 1024
		value = value[:len(value)-2]
	case strings.HasSuffix(lower, "m"):
		multiplier = 1024 * 1024
		value = value[:len(value)-1]
	case strings.HasSuffix(lower, "kib"), strings.HasSuffix(lower, "ki"), strings.HasSuffix(lower, "kb"):
		multiplier = 1024
		value = value[:len(value)-2]
	case strings.HasSuffix(lower, "k"):
		multiplier = 1024
		value = value[:len(value)-1]
	}

	num, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || num <= 0 {
		return 0
	}
	return int64(num * float64(multiplier))
}

func findVolumeMountpoint(ctx context.Context, instanceID string) string {
	cmd := exec.CommandContext(ctx, "podman", "volume", "ls", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, "admiral-"+instanceID) {
			continue
		}

		inspectCmd := exec.CommandContext(ctx, "podman", "volume", "inspect", name, "--format", "{{.Mountpoint}}")
		mpOut, err := inspectCmd.Output()
		if err != nil {
			continue
		}
		mp := strings.TrimSpace(string(mpOut))
		if mp != "" {
			return mp
		}
	}
	return ""
}

func measureDirUsage(ctx context.Context, dir string) int64 {
	cmd := exec.CommandContext(ctx, "du", "-sb", dir)
	out, err := cmd.Output()
	if err != nil {
		return -1
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(parts) < 1 {
		return -1
	}
	bytes, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return -1
	}
	return bytes
}

func classifyStorageState(usedPct float64) (admiral.StorageState, string) {
	switch {
	case usedPct >= 90:
		return admiral.StorageExceeded, fmt.Sprintf("storage usage reached %.1f%% (exceeded threshold)", usedPct)
	case usedPct >= 80:
		return admiral.StorageCritical, fmt.Sprintf("storage usage at %.1f%% (critical threshold)", usedPct)
	case usedPct >= 60:
		return admiral.StorageWarning, fmt.Sprintf("storage usage at %.1f%% (warning threshold)", usedPct)
	default:
		return admiral.StorageOK, ""
	}
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
