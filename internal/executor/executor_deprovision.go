// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) deprovision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if err := security.ValidateInstanceID(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("invalid instance_id: %v", err)
		return result
	}
	var cleanupErrs []error
	for _, unit := range deprovisionUnitNames(task) {
		stopErr := e.systemd().Stop(ctx, unit)
		unitMissing := isMissingUnitDuringDeprovision(stopErr)
		if stopErr != nil {
			if !unitMissing {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("stop unit %q: %w", unit, stopErr))
			}
		}
		// A generated Quadlet unit that systemd reports as not installed
		// cannot be disabled. In particular, systemd-run exposes systemctl's
		// NOTINSTALLED result as its own exit status, while a separate disable
		// attempt only reports a generic FAILURE. Skip that redundant attempt;
		// the Quadlet source is removed after runtime cleanup succeeds below.
		if unitMissing {
			continue
		}
		if err := e.systemd().Disable(ctx, unit); err != nil {
			if !isMissingUnitDuringDeprovision(err) {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("disable unit %q: %w", unit, err))
			}
		}
	}

	// Force-remove the pod before removing service containers and volumes.
	if usesPod(task) {
		if err := e.podman().RemovePod(ctx, podName(task.InstanceID)); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove pod for %q: %w", task.InstanceID, err))
		}
	}

	// Force-remove Podman containers and volumes
	for _, svc := range task.Services {
		cName := containerName(task.InstanceID, svc.Name)
		if err := e.podman().RemoveContainer(ctx, cName); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove container %q: %w", cName, err))
		}
		if svc.Volume != "" {
			vName := volumeName(task.InstanceID, svc.Name)
			if err := e.podman().RemoveVolume(ctx, vName); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("remove volume %q: %w", vName, err))
			}
		}
	}
	for _, shared := range task.SharedVolumes {
		vName := sharedVolumeName(task.InstanceID, shared.Name)
		if err := e.podman().RemoveVolume(ctx, vName); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove shared volume %q: %w", vName, err))
		}
	}

	// Remove Podman secrets before Quadlet files so the secret names
	// are still known (they're derived from the task.Services, not from files).
	if err := e.removePodmanSecrets(ctx, task); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove Podman secrets for %q: %w", task.InstanceID, err))
	}

	// Keep the Quadlet and instance metadata when any managed runtime cleanup
	// fails. They are needed to retry deprovision and to avoid reporting success
	// while Podman resources may still exist.
	if err := errors.Join(cleanupErrs...); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("deprovision instance %q cleanup failed: %v", task.InstanceID, err)
		return result
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

func isMissingUnitDuringDeprovision(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unit not found") ||
		strings.Contains(message, "unit not loaded") ||
		strings.Contains(message, "unit is not loaded") ||
		strings.Contains(message, "unit not available") ||
		strings.Contains(message, "notinstalled") ||
		(strings.Contains(message, "unit file") && strings.Contains(message, "does not exist"))
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
