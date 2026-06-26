// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) provision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if err := validateProvisionTask(task); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	// Idempotency check: if pod already exists, treat as already provisioned.
	podName := podName(task.InstanceID)
	if err := e.podman().PodExists(ctx, podName); err == nil {
		result.Success = true
		result.Logs = fmt.Sprintf("instance %s already provisioned (pod %q exists)", task.InstanceID, podName)
		hostPorts := e.loadHostPorts(e.DataDir, task.InstanceID)
		hostPortsJSON, _ := json.Marshal(hostPorts)
		hasSetup := taskHasSetup(task)
		if hasSetup && task.SetupCompleted {
			hasSetupJSON := "true"
			result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"provision_app","host_ports":%s,"has_setup":%s}`, string(hostPortsJSON), hasSetupJSON)
		} else if hasSetup && e.setupMarkerExists(task.InstanceID) {
			result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"provision_app","host_ports":%s,"has_setup":true}`, string(hostPortsJSON))
		} else {
			result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"provision_app","host_ports":%s}`, string(hostPortsJSON))
		}
		return result
	}

	slog.Info("provision: allocating host ports", "instance", task.InstanceID)
	ports, err := e.allocateHostPorts(e.DataDir, task.InstanceID, task.Services)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("allocate host ports for instance %q: %v", task.InstanceID, err)
		return result
	}
	slog.Info("provision: host ports allocated", "instance", task.InstanceID, "ports", ports)
	r := e.renderer()
	slog.Info("provision: renderer configured", "instance", task.InstanceID, "quadlet_dir", r.QuadletDir, "data_dir", r.DataDir)
	r.HostPorts = ports
	slog.Info("provision: rendering quadlet files", "instance", task.InstanceID)
	if err := r.Render(task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("render quadlet for instance %q: %v", task.InstanceID, err)
		slog.Error("provision: render failed", "instance", task.InstanceID, "error", err)
		return result
	}
	slog.Info("provision: quadlet files rendered", "instance", task.InstanceID)
	slog.Info("provision: chown instance data", "instance", task.InstanceID)
	if err := e.chownInstanceData(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("chown instance data for %q: %v", task.InstanceID, err)
		slog.Error("provision: chown failed", "instance", task.InstanceID, "error", err)
		return result
	}
	slog.Info("provision: creating podman secrets", "instance", task.InstanceID)
	if err := e.createPodmanSecrets(ctx, task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create podman secrets for %q: %v", task.InstanceID, err)
		slog.Error("provision: create secrets failed", "instance", task.InstanceID, "error", err)
		return result
	}
	slog.Info("provision: secrets created, reloading systemd", "instance", task.InstanceID)
	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd for instance %q: %v", task.InstanceID, err)
		slog.Error("provision: daemon-reload failed", "instance", task.InstanceID, "error", err)
		return result
	}
	slog.Info("provision: systemd daemon reloaded", "instance", task.InstanceID)
	for _, svc := range task.Services {
		if svc.Registry != nil {
			loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := e.podman().Login(loginCtx, svc.Registry.Server, svc.Registry.Username, svc.Registry.Password); err != nil {
				cancel()
				result.Success = false
				result.Error = fmt.Sprintf("registry login for service %q: %v", svc.Name, err)
				return result
			}
			cancel()
		}
	}
	for _, unit := range unitNames(task) {
		if err := e.systemd().Start(ctx, unit); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("start unit %q: %v", unit, err)
			return result
		}
	}
	e.writeInstanceTierInfo(e.DataDir, task.InstanceID, task.Tier)

	hostPorts := make(map[string]int)
	for _, svc := range task.Services {
		if svc.Port > 0 {
			infraContainer := executionContainerName(task)
			var hostPort string
			for retry := 0; retry < 10; retry++ {
				p, err := e.podman().PodPort(ctx, infraContainer, fmt.Sprintf("%d/tcp", svc.Port))
				if err == nil {
					hostPort = p
					if hostPort != "" {
						break
					}
				}
				select {
				case <-ctx.Done():
					result.Success = false
					result.Error = fmt.Sprintf("provision cancelled while waiting for pod port: %v", ctx.Err())
					return result
				case <-time.After(1 * time.Second):
				}
			}
			if hostPort != "" {
				if p, err := parsePublishedPort(hostPort); err == nil {
					hostPorts[svc.Name] = p
				}
			}
		}
	}
	hostPortsJSON, _ := json.Marshal(hostPorts)

	// Execute setup_command for each service that declares one.
	// These are one-time initialization commands run via podman exec
	// after the containers are up. Setup runs after all services have
	// started so dependencies are available (depends_on ordering).
	//
	// Setup idempotency has two layers:
	//   1. task.SetupCompleted (populated by admirald from the DB
	//      customer_apps.setup_completed column) is the authoritative
	//      source of truth. If true, setup is always skipped.
	//   2. A local marker file (setup_done) on the node guards against
	//      lost-callback retries: if admirald never received the success
	//      callback and re-dispatches the task with SetupCompleted=false,
	//      the file prevents re-execution on the same node.
	//
	// The DB flag wins: if SetupCompleted=true we skip regardless of the
	// file. If SetupCompleted=false but the file exists, we skip locally
	// (the callback was lost). If both are false/absent we execute setup.
	hasSetup := taskHasSetup(task)
	if hasSetup && !task.SetupCompleted {
		if markerExists := e.setupMarkerExists(task.InstanceID); markerExists {
			hasSetup = true // already ran, report has_setup:true so admirald marks setup_completed
		} else {
			setupErr := e.runSetupCommands(ctx, task, hostPorts)
			if setupErr != nil {
				result.Success = false
				result.Error = fmt.Sprintf("setup_command failed: %v", setupErr)
				result.Logs = fmt.Sprintf("setup_command failed for instance %s: %v", task.InstanceID, setupErr)
				result.Metadata = fmt.Sprintf(
					`{"executor":"systemd-podman","action":"provision_app","host_ports":%s,"has_setup":true,"setup_failed":true,"setup_error":%q}`,
					string(hostPortsJSON), setupErr.Error(),
				)
				return result
			}
			for _, unit := range unitNames(task) {
				slog.Info("provision: restarting unit after setup", "instance", task.InstanceID, "unit", unit)
				if err := e.systemd().Restart(ctx, unit); err != nil {
					result.Success = false
					result.Error = fmt.Sprintf("restart unit %q after setup: %v", unit, err)
					result.Logs = fmt.Sprintf("restart unit %s after setup failed for instance %s: %v", unit, task.InstanceID, err)
					result.Metadata = fmt.Sprintf(
						`{"executor":"systemd-podman","action":"provision_app","host_ports":%s,"has_setup":true,"setup_failed":true,"setup_error":%q}`,
						string(hostPortsJSON), err.Error(),
					)
					return result
				}
			}
			for _, svc := range task.Services {
				if err := e.waitForServiceReady(ctx, task.InstanceID, svc, hostPorts); err != nil {
					result.Success = false
					result.Error = fmt.Sprintf("wait for service %q after setup restart: %v", svc.Name, err)
					result.Logs = fmt.Sprintf("service %s was not ready after setup restart for instance %s: %v", svc.Name, task.InstanceID, err)
					result.Metadata = fmt.Sprintf(
						`{"executor":"systemd-podman","action":"provision_app","host_ports":%s,"has_setup":true,"setup_failed":true,"setup_error":%q}`,
						string(hostPortsJSON), err.Error(),
					)
					return result
				}
			}
			e.writeSetupMarker(task.InstanceID)
		}
	}
	if task.SetupCompleted {
		hasSetup = true
	}

	hasSetupJSON := "false"
	if hasSetup {
		hasSetupJSON = "true"
	}
	result.Success = true
	result.Logs = fmt.Sprintf("provisioned instance %s", task.InstanceID)
	result.Metadata = fmt.Sprintf(
		`{"executor":"systemd-podman","action":"provision_app","host_ports":%s,"has_setup":%s}`,
		string(hostPortsJSON), hasSetupJSON,
	)
	return result
}

// taskHasSetup returns true if any service in the task declares a
// setup_command.
func taskHasSetup(task admiral.FleetTask) bool {
	for _, svc := range task.Services {
		if strings.TrimSpace(svc.SetupCommand) != "" {
			return true
		}
	}
	return false
}

// setupMarkerPath returns the path to the local marker file that
// indicates setup_command has already been executed on this node.
func setupMarkerPath(dataDir, instanceID string) string {
	return filepath.Join(dataDir, "instances", instanceID, "setup_done")
}

// setupMarkerExists returns true if the local setup_done marker file
// exists for the given instance.
func (e *SystemdPodmanExecutor) setupMarkerExists(instanceID string) bool {
	if e.FS == nil {
		return false
	}
	if _, err := e.FS.Stat(setupMarkerPath(e.DataDir, instanceID)); err != nil {
		return false
	}
	return true
}

// writeSetupMarker writes the local setup_done marker file so that a
// lost-callback retry does not re-execute setup_command on this node.
func (e *SystemdPodmanExecutor) writeSetupMarker(instanceID string) {
	dir := filepath.Join(e.DataDir, "instances", instanceID)
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		return
	}
	_ = e.FS.WriteFile(setupMarkerPath(e.DataDir, instanceID), []byte("done"), 0600)
}

// runSetupCommands executes the setup_command declared on each service
// (if any) via podman exec inside the running container. Returns an
// error if any setup command fails.
//
// Commands are executed with "sh -c" so the setup_command can use shell
// features such as environment variable expansion ($VAR), redirection,
// and quoting. The shell runs inside the container and the command
// string comes from a trusted app definition (audited DB data), not from
// end-user input.
//
// The timeout is derived from each service's SetupTimeout field.
// If not set, a default of 10 minutes is applied.
func (e *SystemdPodmanExecutor) runSetupCommands(ctx context.Context, task admiral.FleetTask, hostPorts map[string]int) error {
	servicesByName := make(map[string]admiral.ServiceInfo, len(task.Services))
	for _, svc := range task.Services {
		servicesByName[svc.Name] = svc
	}
	for _, svc := range task.Services {
		if strings.TrimSpace(svc.SetupCommand) == "" {
			continue
		}
		for _, depName := range setupDependencyNames(svc) {
			depSvc, ok := servicesByName[depName]
			if !ok {
				continue
			}
			if err := e.waitForServiceReady(ctx, task.InstanceID, depSvc, hostPorts); err != nil {
				return fmt.Errorf("wait for dependency service %q: %w", depName, err)
			}
		}
		if err := e.waitForServiceReady(ctx, task.InstanceID, svc, hostPorts); err != nil {
			return fmt.Errorf("wait for setup service %q: %w", svc.Name, err)
		}
		setupTimeout := svc.SetupTimeout
		if setupTimeout <= 0 {
			setupTimeout = 600
		}
		setupCtx, cancel := context.WithTimeout(ctx, time.Duration(setupTimeout)*time.Second)
		out, err := e.runServiceSetupTrustedShell(setupCtx, task.InstanceID, svc, svc.SetupCommand, time.Duration(setupTimeout)*time.Second)
		cancel()
		if err != nil {
			return fmt.Errorf("setup_command for service %q: %w: %s", svc.Name, err, string(out))
		}
	}
	return nil
}

func setupDependencyNames(svc admiral.ServiceInfo) []string {
	seen := make(map[string]struct{}, len(svc.DependsOn)+len(svc.Requires))
	names := make([]string, 0, len(svc.DependsOn)+len(svc.Requires))
	for _, depName := range svc.Requires {
		if _, ok := seen[depName]; ok {
			continue
		}
		seen[depName] = struct{}{}
		names = append(names, depName)
	}
	for _, depName := range svc.DependsOn {
		if _, ok := seen[depName]; ok {
			continue
		}
		seen[depName] = struct{}{}
		names = append(names, depName)
	}
	return names
}

func (e *SystemdPodmanExecutor) waitForServiceReady(ctx context.Context, instanceID string, svc admiral.ServiceInfo, hostPorts map[string]int) error {
	interval := 2 * time.Second
	timeout := 5 * time.Second
	attempts := 60
	if svc.HealthCheck != nil {
		if svc.HealthCheck.IntervalSeconds > 0 {
			interval = time.Duration(svc.HealthCheck.IntervalSeconds) * time.Second
		}
		if svc.HealthCheck.TimeoutSeconds > 0 {
			timeout = time.Duration(svc.HealthCheck.TimeoutSeconds) * time.Second
		}
		if svc.HealthCheck.FailureThreshold > 0 {
			attempts = svc.HealthCheck.FailureThreshold
		}
	}
	if svc.HealthCheckWaitSecs > 0 {
		attempts = svc.HealthCheckWaitSecs / int(interval.Seconds())
		if attempts < 1 {
			attempts = 1
		}
	}

	container := containerName(instanceID, svc.Name)
	for retry := 0; retry < attempts; retry++ {
		ready, err := e.serviceReadyCheck(ctx, instanceID, container, svc, hostPorts, timeout)
		if err == nil && ready {
			return nil
		}
		if err != nil {
			slog.Error("serviceReadyCheck failed",
				"instance", instanceID,
				"service", svc.Name,
				"container", container,
				"attempt", retry+1,
				"max_attempts", attempts,
				"error", err,
			)
		} else if !ready {
			slog.Warn("service not ready yet",
				"instance", instanceID,
				"service", svc.Name,
				"container", container,
				"attempt", retry+1,
				"max_attempts", attempts,
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("service %q did not reach ready state in time", svc.Name)
}

func (e *SystemdPodmanExecutor) serviceReadyCheck(ctx context.Context, instanceID, container string, svc admiral.ServiceInfo, hostPorts map[string]int, timeout time.Duration) (bool, error) {
	if err := e.podman().ContainerExists(ctx, container); err != nil {
		return false, err
	}
	inspect, err := e.podman().ContainerInspect(ctx, container)
	if err != nil {
		return false, err
	}
	status := extractContainerStatus(inspect)
	if status != "running" {
		slog.Error("container not running",
			"instance", instanceID,
			"container", container,
			"status", status,
		)
		return false, fmt.Errorf("container %q is not running (status: %s)", container, status)
	}
	if svc.HealthCheck == nil {
		return true, nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch strings.ToLower(strings.TrimSpace(svc.HealthCheck.Type)) {
	case "", "none":
		return true, nil
	case "command":
		if len(svc.HealthCheck.Command) == 0 {
			return false, fmt.Errorf("service %q command healthcheck requires command", svc.Name)
		}
		slog.Debug("running command healthcheck", "service", svc.Name, "instance", instanceID, "command", svc.HealthCheck.Command)
		out, err := e.runServiceCommandNoEntrypoint(checkCtx, instanceID, svc, svc.HealthCheck.Command...)
		if err != nil {
			slog.Warn("command healthcheck failed", "service", svc.Name, "instance", instanceID, "error", err, "output", string(out))
			return false, fmt.Errorf("service %q command healthcheck failed: %w: %s", svc.Name, err, string(out))
		}
		slog.Debug("command healthcheck succeeded", "service", svc.Name, "instance", instanceID)
		return true, nil
	case "tcp":
		hostPort, ok := hostPorts[svc.Name]
		if !ok || hostPort <= 0 {
			return false, fmt.Errorf("service %q tcp healthcheck requires a published port", svc.Name)
		}
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(checkCtx, "tcp", fmt.Sprintf("127.0.0.1:%d", hostPort))
		if err != nil {
			return false, err
		}
		_ = conn.Close()
		return true, nil
	case "http":
		hostPort, ok := hostPorts[svc.Name]
		if !ok || hostPort <= 0 {
			return false, fmt.Errorf("service %q http healthcheck requires a published port", svc.Name)
		}
		path := svc.HealthCheck.Path
		if path == "" {
			path = "/"
		}
		expectedStatus := svc.HealthCheck.ExpectedStatus
		if expectedStatus == 0 {
			expectedStatus = http.StatusOK
		}
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", hostPort, path), nil)
		if err != nil {
			return false, err
		}
		resp, err := (&http.Client{Timeout: timeout}).Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != expectedStatus {
			return false, fmt.Errorf("service %q http healthcheck returned %d, expected %d", svc.Name, resp.StatusCode, expectedStatus)
		}
		return true, nil
	default:
		return false, fmt.Errorf("service %q healthcheck type %q is unsupported", svc.Name, svc.HealthCheck.Type)
	}
}

func extractContainerStatus(inspect []byte) string {
	// Try to extract the State.Status from the JSON output.
	// podman container inspect returns an array of container objects.
	var containers []struct {
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
	}
	if err := json.Unmarshal(inspect, &containers); err == nil && len(containers) > 0 {
		return containers[0].State.Status
	}
	// Fallback: extract a quoted string after "status" in the raw JSON
	raw := string(inspect)
	idx := strings.Index(strings.ToLower(raw), `"status"`)
	if idx >= 0 {
		after := raw[idx+8:] // skip "status"
		after = strings.TrimSpace(after)
		if strings.HasPrefix(after, ":") {
			after = strings.TrimSpace(after[1:])
			if len(after) > 0 && after[0] == '"' {
				end := strings.IndexByte(after[1:], '"')
				if end >= 0 {
					return after[1 : end+1]
				}
			}
		}
	}
	return "unknown"
}

func validateProvisionTask(task admiral.FleetTask) error {
	if strings.TrimSpace(task.InstanceID) == "" {
		return fmt.Errorf("instance_id is required")
	}
	if !hasValidTaskMemoryLimit(task.Tier.Memory) {
		return fmt.Errorf("provision_app requires a valid memory limit, got %q", task.Tier.Memory)
	}
	if len(task.Services) == 0 {
		return fmt.Errorf("provision_app requires at least one service")
	}
	for _, svc := range task.Services {
		if strings.TrimSpace(svc.Name) == "" {
			return fmt.Errorf("service name is required")
		}
		if strings.TrimSpace(svc.Image) == "" {
			return fmt.Errorf("service %q image is required", svc.Name)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) writeInstanceTierInfo(dataDir, instanceID string, tier admiral.TierInfo) {
	dir := filepath.Join(dataDir, "instances", instanceID)
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		return
	}
	info := map[string]interface{}{
		"cpu":     tier.CPU,
		"memory":  tier.Memory,
		"storage": tier.Storage,
	}
	data, err := json.Marshal(info)
	if err != nil {
		return
	}
	_ = e.FS.WriteFile(filepath.Join(dir, "tier.json"), data, 0600)
}

const minHostPort = 40000
const maxHostPort = 49999

func (e *SystemdPodmanExecutor) allocateHostPorts(dataDir, instanceID string, services []admiral.ServiceInfo) (map[string]int, error) {
	// Idempotency: if ports already allocated for this instance, reuse them.
	if existing := e.loadHostPorts(dataDir, instanceID); existing != nil {
		return existing, nil
	}

	dir := filepath.Join(dataDir, "instances", instanceID)
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create instance dir for port allocation: %w", err)
	}
	counterFile := filepath.Join(dataDir, "next_port")
	next := minHostPort
	if data, err := e.FS.ReadFile(counterFile); err == nil {
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &next); err != nil {
			next = minHostPort
		}
	}
	if next < minHostPort {
		next = minHostPort
	}
	ports := make(map[string]int, len(services))
	for _, svc := range services {
		if svc.Port == 0 {
			continue
		}
		if next > maxHostPort {
			return nil, fmt.Errorf("no available host ports in range %d-%d", minHostPort, maxHostPort)
		}
		ports[svc.Name] = next
		next++
	}
	if err := e.FS.WriteFile(counterFile, []byte(fmt.Sprintf("%d", next)), 0644); err != nil {
		return nil, fmt.Errorf("persist next port: %w", err)
	}
	portData, err := json.Marshal(ports)
	if err != nil {
		return nil, fmt.Errorf("marshal ports: %w", err)
	}
	if err := e.FS.WriteFile(portsFilePath(dataDir, instanceID), portData, 0600); err != nil {
		return nil, fmt.Errorf("write ports file: %w", err)
	}
	return ports, nil
}

func (e *SystemdPodmanExecutor) loadHostPorts(dataDir, instanceID string) map[string]int {
	data, err := e.FS.ReadFile(portsFilePath(dataDir, instanceID))
	if err != nil {
		return nil
	}
	var ports map[string]int
	if err := json.Unmarshal(data, &ports); err != nil {
		return nil
	}
	return ports
}
