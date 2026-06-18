// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) inspect(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	snapshot, err := e.inspectSnapshot(ctx, task)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("marshal inspect snapshot for instance %q: %v", task.InstanceID, err)
		return result
	}
	result.Success = true
	result.Logs = fmt.Sprintf("inspected instance %s", task.InstanceID)
	result.Metadata = string(payload)
	return result
}

func (e *SystemdPodmanExecutor) inspectSnapshot(ctx context.Context, task admiral.FleetTask) (map[string]interface{}, error) {
	services := make([]map[string]interface{}, 0, len(task.Services))
	for _, svc := range task.Services {
		containerName := containerName(task.InstanceID, svc.Name)
		containerInspect, err := e.podman().ContainerInspect(ctx, containerName)
		if err != nil {
			return nil, fmt.Errorf("inspect container %q: %w", containerName, err)
		}

		unitName := quadlet.ContainerUnitName(task.InstanceID, svc.Name)
		unitStatus, _ := e.systemd().Status(ctx, unitName)

		serviceSnapshot := map[string]interface{}{
			"name":              svc.Name,
			"image":             svc.Image,
			"container":         containerName,
			"container_unit":    unitName,
			"container_status":  strings.TrimSpace(string(unitStatus)),
			"container_inspect": mustJSONValue(containerInspect),
		}
		if svc.Volume != "" {
			volName := volumeName(task.InstanceID, svc.Name)
			volumeInspect, err := e.podman().VolumeInspect(ctx, volName)
			if err != nil {
				return nil, fmt.Errorf("inspect volume %q: %w", volName, err)
			}
			serviceSnapshot["volume"] = map[string]interface{}{
				"name":    volName,
				"source":  svc.Volume,
				"inspect": mustJSONValue(volumeInspect),
			}
		}
		services = append(services, serviceSnapshot)
	}

	containers, _ := e.podman().ContainerPS(ctx)

	return map[string]interface{}{
		"executor":       "systemd-podman",
		"instance_id":    task.InstanceID,
		"containers":     services,
		"all_containers": mustJSONValue(containers),
		"inspected_at":   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func mustJSONValue(data []byte) interface{} {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	return v
}

func (e *SystemdPodmanExecutor) startMetadata(ctx context.Context, task admiral.FleetTask) (string, error) {
	hostPorts := make(map[string]int)
	infraContainer := containerName(task.InstanceID, "infra")
	for _, svc := range task.Services {
		if svc.Port > 0 {
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
					return "", fmt.Errorf("start metadata cancelled while waiting for pod port: %w", ctx.Err())
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
	return fmt.Sprintf(`{"executor":"systemd-podman","action":"start_app","host_ports":%s}`, string(hostPortsJSON)), nil
}
