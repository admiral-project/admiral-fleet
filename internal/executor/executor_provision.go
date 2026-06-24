// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"fmt"
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
		result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"provision_app","host_ports":%s}`, string(hostPortsJSON))
		return result
	}

	ports, err := e.allocateHostPorts(e.DataDir, task.InstanceID, task.Services)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("allocate host ports for instance %q: %v", task.InstanceID, err)
		return result
	}
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("render quadlet for instance %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.chownInstanceData(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("chown instance data for %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.createPodmanSecrets(ctx, task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create podman secrets for %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd for instance %q: %v", task.InstanceID, err)
		return result
	}
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

	result.Success = true
	result.Logs = fmt.Sprintf("provisioned instance %s", task.InstanceID)
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"provision_app","host_ports":%s}`, string(hostPortsJSON))
	return result
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
