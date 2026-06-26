// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) deprovision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	for _, unit := range deprovisionUnitNames(task) {
		if err := e.systemd().Stop(ctx, unit); err != nil {
			slog.Debug("stop unit during deprovision", "unit", unit, "error", err)
		}
		if err := e.systemd().Disable(ctx, unit); err != nil {
			slog.Debug("disable unit during deprovision", "unit", unit, "error", err)
		}
	}

	// Force-remove the pod before removing service containers and volumes.
	if usesPod(task) {
		if err := e.podman().RemovePod(ctx, podName(task.InstanceID)); err != nil {
			slog.Debug("remove pod during deprovision", "instance", task.InstanceID, "error", err)
		}
	}

	// Force-remove Podman containers and volumes
	for _, svc := range task.Services {
		cName := containerName(task.InstanceID, svc.Name)
		if err := e.podman().RemoveContainer(ctx, cName); err != nil {
			slog.Debug("remove container during deprovision", "container", cName, "error", err)
		}
		if svc.Volume != "" {
			vName := volumeName(task.InstanceID, svc.Name)
			if err := e.podman().RemoveVolume(ctx, vName); err != nil {
				slog.Debug("remove volume during deprovision", "volume", vName, "error", err)
			}
		}
	}
	for _, shared := range task.SharedVolumes {
		vName := sharedVolumeName(task.InstanceID, shared.Name)
		if err := e.podman().RemoveVolume(ctx, vName); err != nil {
			slog.Debug("remove shared volume during deprovision", "volume", vName, "error", err)
		}
	}

	// Remove Podman secrets before Quadlet files so the secret names
	// are still known (they're derived from the task.Services, not from files).
	e.removePodmanSecrets(ctx, task)

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
	if err := e.systemd().ResetFailed(ctx); err != nil {
		slog.Debug("reset failed systemd during deprovision", "instance", task.InstanceID, "error", err)
	}

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
