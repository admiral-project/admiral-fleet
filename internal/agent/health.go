// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type healthReport struct {
	InstanceID   string         `json:"instance_id"`
	NodeID       string         `json:"node_id"`
	BootID       string         `json:"boot_id"`
	HealthStatus string         `json:"health_status"`
	Message      string         `json:"message,omitempty"`
	HostPorts    map[string]int `json:"host_ports,omitempty"`
	CheckedAt    string         `json:"checked_at"`
}

const workloadHealthCheckInterval = 5 * time.Second
const rootlessManagerCheckInterval = 500 * time.Millisecond

type rootlessManagerStatus struct {
	PID         string
	ActiveState string
}

func (a *Agent) StartHealthChecker(ctx context.Context) {
	managerStatus := rootlessManagerStatus{}
	managerAvailable := a.rootlessManagerState == nil
	if a.rootlessManagerState != nil {
		status, err := a.rootlessManagerState(ctx)
		if err != nil {
			slog.Warn("could not read rootless systemd manager state", "node_id", a.NodeID, "error", err)
		} else {
			managerStatus = status
			managerAvailable = status.ActiveState == "active" && status.PID != "0"
		}
	}
	if managerAvailable {
		a.checkAllPods(ctx)
	} else {
		a.reportUnavailableQuadletPods(ctx, "rootless systemd manager unavailable; awaiting restart")
	}

	// Keep the stale-health window for individual service failures bounded.
	// The rootless manager PID does not change when one workload unit stops,
	// so periodic Podman reconciliation must detect that transition promptly.
	healthTicker := time.NewTicker(workloadHealthCheckInterval)
	defer healthTicker.Stop()
	managerTicker := time.NewTicker(rootlessManagerCheckInterval)
	defer managerTicker.Stop()
	observeManager := func() bool {
		if a.rootlessManagerState == nil {
			return true
		}
		status, err := a.rootlessManagerState(ctx)
		if err != nil {
			slog.Debug("could not read rootless systemd manager state", "node_id", a.NodeID, "error", err)
			if managerAvailable {
				managerAvailable = false
				a.reportUnavailableQuadletPods(ctx, "rootless systemd manager state unavailable; awaiting recovery")
			}
			return false
		}
		if status.ActiveState != "active" || status.PID == "0" {
			if managerAvailable {
				slog.Info("rootless systemd manager became unavailable; invalidating workload health", "node_id", a.NodeID, "manager_pid", status.PID, "manager_state", status.ActiveState)
				managerStatus = status
				managerAvailable = false
				a.reportUnavailableQuadletPods(ctx, "rootless systemd manager unavailable; awaiting restart")
			}
			return false
		}
		if !managerAvailable || status.PID != managerStatus.PID {
			previousPID := managerStatus.PID
			managerStatus = status
			managerAvailable = true
			slog.Info("rootless systemd manager active; awaiting fresh workload health", "node_id", a.NodeID, "previous_pid", previousPID, "manager_pid", status.PID)
			a.refreshHealthAfterRootlessManagerChange(ctx)
			healthTicker.Stop()
			select {
			case <-healthTicker.C:
			default:
			}
			healthTicker.Reset(workloadHealthCheckInterval)
			return false
		}
		managerStatus = status
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-healthTicker.C:
			if observeManager() {
				a.checkAllPods(ctx)
			}
		case <-managerTicker.C:
			observeManager()
		}
	}
}

func (a *Agent) refreshHealthAfterRootlessManagerChange(ctx context.Context) {
	a.reportUnavailableQuadletPods(ctx, "rootless systemd manager active; awaiting fresh workload health")
}

func readRootlessManagerStatus(ctx context.Context, rootlessUser string) (rootlessManagerStatus, error) {
	account, err := user.Lookup(rootlessUser)
	if err != nil {
		return rootlessManagerStatus{}, fmt.Errorf("lookup rootless user %q: %w", rootlessUser, err)
	}
	unit := "user@" + account.Uid + ".service"
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "systemctl", "show", unit, "--property=MainPID", "--property=ActiveState", "--no-pager")
	output, err := cmd.Output()
	if err != nil {
		return rootlessManagerStatus{}, fmt.Errorf("read systemd manager state for %s: %w", unit, err)
	}
	var status rootlessManagerStatus
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "MainPID":
			status.PID = strings.TrimSpace(value)
		case "ActiveState":
			status.ActiveState = strings.TrimSpace(value)
		}
	}
	if status.PID == "" {
		return rootlessManagerStatus{}, fmt.Errorf("systemd manager PID for %s is empty", unit)
	}
	pid, err := strconv.Atoi(status.PID)
	if err != nil || pid < 0 {
		return rootlessManagerStatus{}, fmt.Errorf("systemd manager PID for %s is invalid: %q", unit, status.PID)
	}
	if status.ActiveState == "" {
		return rootlessManagerStatus{}, fmt.Errorf("systemd manager active state for %s is empty", unit)
	}
	return status, nil
}

func (a *Agent) reportUnavailableQuadletPods(ctx context.Context, message string) {
	for _, instanceID := range a.listQuadletPodFiles() {
		report := healthReport{
			InstanceID:   instanceID,
			NodeID:       a.NodeID,
			BootID:       a.currentBootID(),
			HealthStatus: "stopped",
			Message:      message,
			CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		if err := a.postHealth(report); err != nil {
			slog.Warn("health report failed during rootless manager change", "instance_id", instanceID, "error", err)
		}
	}
}

func (a *Agent) checkAllPods(ctx context.Context) {
	runningPods := make(map[string]string)

	pods, err := a.listAdmiralPods(ctx)
	if err != nil {
		return
	}
	containerStates, containerErr := a.listAdmiralContainerStates(ctx)

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
		} else if healthy, serviceMessage := checkInstanceServiceContainers(a.QuadletDir, instanceID, containerStates, containerErr); !healthy {
			status = "unhealthy"
			msg = serviceMessage
		} else if healthy, serviceMessage := a.checkDeclaredServiceHealth(ctx, instanceID); !healthy {
			status = "unhealthy"
			msg = serviceMessage
		}

		report := healthReport{
			InstanceID:   instanceID,
			NodeID:       a.NodeID,
			BootID:       a.currentBootID(),
			HealthStatus: status,
			Message:      msg,
			CheckedAt:    time.Now().UTC().Format(time.RFC3339),
		}

		if status == "healthy" {
			report.HostPorts = a.detectHostPorts(instanceID)
		}

		if err := a.postHealth(report); err != nil {
			slog.Warn("health report failed", "instance_id", report.InstanceID, "error", err)
		}
	}

	quadletPods := a.listQuadletPodFiles()
	for _, instanceID := range quadletPods {
		if _, ok := runningPods[instanceID]; !ok {
			report := healthReport{
				InstanceID:   instanceID,
				NodeID:       a.NodeID,
				BootID:       a.currentBootID(),
				HealthStatus: "stopped",
				Message:      "pod not running (Quadlet file exists but no pod found)",
				CheckedAt:    time.Now().UTC().Format(time.RFC3339),
			}
			if err := a.postHealth(report); err != nil {
				slog.Warn("health report failed", "instance_id", report.InstanceID, "error", err)
			}
		}
	}
}

func (a *Agent) checkDeclaredServiceHealth(ctx context.Context, instanceID string) (bool, string) {
	dataDir := strings.TrimSpace(a.DataDir)
	if dataDir == "" {
		dataDir = envOrDefault("ADMIRAL_FLEET_DATA_DIR", "/var/lib/admiral")
	}
	if err := security.ValidateInstanceID(instanceID); err != nil {
		return false, "invalid workload identity for service health checks"
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "instances", instanceID, "healthchecks.json"))
	if os.IsNotExist(err) {
		return true, ""
	}
	if err != nil {
		return false, "failed to read declared service health checks"
	}
	var checks []admiral.ServiceHealthCheck
	if err := json.Unmarshal(data, &checks); err != nil {
		return false, "failed to parse declared service health checks"
	}
	for _, check := range checks {
		if err := a.runDeclaredServiceHealthCheck(ctx, instanceID, check); err != nil {
			return false, fmt.Sprintf("service %q health check failed", check.ServiceName)
		}
	}
	return true, ""
}

func (a *Agent) runDeclaredServiceHealthCheck(ctx context.Context, instanceID string, check admiral.ServiceHealthCheck) error {
	if strings.TrimSpace(check.ServiceName) == "" {
		return fmt.Errorf("service name is empty")
	}
	timeout := 5 * time.Second
	if check.HealthCheck.TimeoutSeconds > 0 {
		timeout = time.Duration(check.HealthCheck.TimeoutSeconds) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch strings.ToLower(strings.TrimSpace(check.HealthCheck.Type)) {
	case "", "none":
		return nil
	case "command":
		if len(check.HealthCheck.Command) == 0 {
			return fmt.Errorf("command health check is empty")
		}
		args := []string{"exec", "admiral-" + instanceID + "-" + check.ServiceName}
		args = append(args, check.HealthCheck.Command...)
		if _, err := a.runPodman(checkCtx, args...); err != nil {
			return fmt.Errorf("run service command health check: %w", err)
		}
		return nil
	case "tcp":
		if check.HostPort <= 0 {
			return fmt.Errorf("TCP health check has no published port")
		}
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(checkCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(check.HostPort)))
		if err != nil {
			return err
		}
		return conn.Close()
	case "http":
		if check.HostPort <= 0 {
			return fmt.Errorf("HTTP health check has no published port")
		}
		path := check.HealthCheck.Path
		if path == "" {
			path = "/"
		}
		request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", check.HostPort, path), nil)
		if err != nil {
			return fmt.Errorf("create HTTP health check request: %w", err)
		}
		response, err := (&http.Client{Timeout: timeout}).Do(request)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		expected := check.HealthCheck.ExpectedStatus
		if expected == 0 {
			expected = http.StatusOK
		}
		if response.StatusCode != expected {
			return fmt.Errorf("HTTP health check returned %d, expected %d", response.StatusCode, expected)
		}
		return nil
	default:
		return fmt.Errorf("unsupported health check type %q", check.HealthCheck.Type)
	}
}

func checkInstanceServiceContainers(quadletDir, instanceID string, containerStates map[string]string, inspectErr error) (bool, string) {
	if quadletDir == "" {
		quadletDir = "/etc/containers/systemd/admiral"
	}
	if inspectErr != nil {
		return false, "failed to inspect workload containers"
	}
	instanceID = sanitizeInstanceID(instanceID)
	pattern := filepath.Join(quadletDir, "admiral-"+instanceID+"-*.container")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return false, "failed to enumerate declared service units"
	}
	if len(files) == 0 {
		return false, "no declared service units found for running pod"
	}

	for _, file := range files {
		containerName := strings.TrimSuffix(filepath.Base(file), ".container")
		status, ok := containerStates[containerName]
		if !ok {
			return false, fmt.Sprintf("service container %q is unavailable", containerName)
		}
		if strings.ToLower(strings.TrimSpace(status)) != "running" {
			return false, fmt.Sprintf("service container %q is not running", containerName)
		}
	}
	return true, ""
}

func (a *Agent) listAdmiralContainerStates(ctx context.Context) (map[string]string, error) {
	output, err := a.runPodman(ctx, "ps", "--all", "--format", "{{.Names}}\t{{.State}}")
	if err != nil {
		return nil, fmt.Errorf("list workload containers: %w", err)
	}
	states := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "admiral-") {
			continue
		}
		states[fields[0]] = strings.TrimSpace(fields[1])
	}
	return states, nil
}

func (a *Agent) detectHostPorts(instanceID string) map[string]int {
	dataDir := os.Getenv("ADMIRAL_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/admiral"
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "instances", sanitizeInstanceID(instanceID), "ports.json")) // #nosec G304 -- path is sanitized and follows controlled structure
	if err != nil {
		return nil
	}
	var ports map[string]int
	if err := json.Unmarshal(data, &ports); err != nil {
		return nil
	}
	return ports
}

func (a *Agent) listQuadletPodFiles() []string {
	pattern := a.QuadletDir
	if pattern == "" {
		pattern = "/etc/containers/systemd/admiral"
	}
	matches, err := filepath.Glob(filepath.Join(pattern, "*.pod"))
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

func (a *Agent) listAdmiralPods(ctx context.Context) ([]podInfo, error) {
	out, err := a.runPodman(ctx, "pod", "ps", "--format", "{{.Name}}\t{{.Status}}")
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	pods := make([]podInfo, 0, len(lines))
	for _, line := range lines {
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

func sanitizeInstanceID(id string) string {
	s := filepath.Clean(id)
	s = strings.ReplaceAll(s, "..", "")
	s = strings.ReplaceAll(s, "/", "")
	return s
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

	startup := time.NewTimer(30 * time.Second)
	defer startup.Stop()
	select {
	case <-ctx.Done():
		return
	case <-startup.C:
	}

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

func (a *Agent) checkInstanceStorage(ctx context.Context) {
	instances := a.listQuadletPodFiles()
	if len(instances) == 0 {
		return
	}

	for _, instanceID := range instances {
		report := a.measureInstanceStorage(ctx, instanceID)
		if report == nil {
			continue
		}
		if err := a.postStorage(*report); err != nil {
			slog.Warn("storage report failed", "instance_id", report.InstanceID, "error", err)
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
	mountpoint := a.findVolumeMountpoint(ctx, instanceID)
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
	safeID := sanitizeInstanceID(instanceID)
	paths := []string{
		filepath.Join("/var/lib/admiral/instances", safeID, "tier.json"),
		filepath.Join("/var/lib/admiral/instances", safeID, "instance.json"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p) // #nosec G304 -- paths are built from controlled instance metadata locations
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
	unitLen := 0

	switch {
	case strings.HasSuffix(lower, "tib"):
		multiplier = 1024 * 1024 * 1024 * 1024
		unitLen = 3
	case strings.HasSuffix(lower, "ti"):
		multiplier = 1024 * 1024 * 1024 * 1024
		unitLen = 2
	case strings.HasSuffix(lower, "tb"):
		multiplier = 1000 * 1000 * 1000 * 1000
		unitLen = 2
	case strings.HasSuffix(lower, "t"):
		multiplier = 1024 * 1024 * 1024 * 1024
		unitLen = 1
	case strings.HasSuffix(lower, "gib"):
		multiplier = 1024 * 1024 * 1024
		unitLen = 3
	case strings.HasSuffix(lower, "gi"):
		multiplier = 1024 * 1024 * 1024
		unitLen = 2
	case strings.HasSuffix(lower, "gb"):
		multiplier = 1000 * 1000 * 1000
		unitLen = 2
	case strings.HasSuffix(lower, "g"):
		multiplier = 1024 * 1024 * 1024
		unitLen = 1
	case strings.HasSuffix(lower, "mib"):
		multiplier = 1024 * 1024
		unitLen = 3
	case strings.HasSuffix(lower, "mi"):
		multiplier = 1024 * 1024
		unitLen = 2
	case strings.HasSuffix(lower, "mb"):
		multiplier = 1000 * 1000
		unitLen = 2
	case strings.HasSuffix(lower, "m"):
		multiplier = 1024 * 1024
		unitLen = 1
	case strings.HasSuffix(lower, "kib"):
		multiplier = 1024
		unitLen = 3
	case strings.HasSuffix(lower, "ki"):
		multiplier = 1024
		unitLen = 2
	case strings.HasSuffix(lower, "kb"):
		multiplier = 1024
		unitLen = 2
	case strings.HasSuffix(lower, "k"):
		multiplier = 1024
		unitLen = 1
	default:
		return 0
	}

	if unitLen > 0 && unitLen < len(value) {
		value = value[:len(value)-unitLen]
	} else {
		return 0
	}

	num, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || num <= 0 {
		return 0
	}
	return int64(num * float64(multiplier))
}

func (a *Agent) findVolumeMountpoint(ctx context.Context, instanceID string) string {
	out, err := a.runPodman(ctx, "volume", "ls", "--format", "{{.Name}}")
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

		mpOut, err := a.runPodman(ctx, "volume", "inspect", name, "--format", "{{.Mountpoint}}")
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
	cmd := exec.CommandContext(ctx, "du", "-sb", dir) // #nosec G204 -- dir comes from podman volume inspection for an existing mountpoint
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
	case usedPct >= 100:
		return admiral.StorageOverQuota, fmt.Sprintf("storage usage at %.1f%% (over quota)", usedPct)
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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.APIURL+"/api/v1/fleet/health", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("send health: %w", err)
	}
	defer drainAndCloseResponse(resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health callback failed with HTTP %d", resp.StatusCode)
	}
	return nil
}
