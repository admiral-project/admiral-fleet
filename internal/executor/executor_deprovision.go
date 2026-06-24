// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) deprovision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	for _, unit := range deprovisionUnitNames(task) {
		_ = e.systemd().Stop(ctx, unit)
		_ = e.systemd().Disable(ctx, unit)
	}

	// Force-remove the pod before removing service containers and volumes.
	if usesPod(task) {
		_ = e.podman().RemovePod(ctx, podName(task.InstanceID))
	}

	// Force-remove Podman containers and volumes
	for _, svc := range task.Services {
		cName := containerName(task.InstanceID, svc.Name)
		_ = e.podman().RemoveContainer(ctx, cName)
		if svc.Volume != "" {
			vName := volumeName(task.InstanceID, svc.Name)
			_ = e.podman().RemoveVolume(ctx, vName)
		}
	}
	for _, shared := range task.SharedVolumes {
		_ = e.podman().RemoveVolume(ctx, sharedVolumeName(task.InstanceID, shared.Name))
	}

	// Remove Quadlet files
	if err := e.renderer().Remove(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("remove quadlet files for %q: %v", task.InstanceID, err)
		return result
	}

	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd after deprovision %q: %v", task.InstanceID, err)
		return result
	}

	// Reset failed systemd states
	_ = e.systemd().ResetFailed(ctx)

	// Clean up instance data dir (ports.json, env files)
	dataDir := e.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/var/lib/admiral"
	}
	instDir := filepath.Join(dataDir, "instances", task.InstanceID)
	if err := e.FS.RemoveAll(instDir); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("clean up instance data dir %q: %v", instDir, err)
		return result
	}

	result.Success = true
	result.Logs = fmt.Sprintf("deprovisioned instance %s", task.InstanceID)
	return result
}

func deprovisionUnitNames(task admiral.FleetTask) []string {
	units := unitNames(task)
	for _, svc := range task.Services {
		if svc.Volume != "" {
			units = append(units, quadlet.VolumeUnitName(task.InstanceID, svc.Name))
		}
	}
	for _, shared := range task.SharedVolumes {
		units = append(units, quadlet.SharedVolumeUnitName(task.InstanceID, shared.Name))
	}
	return units
}
