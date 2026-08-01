// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func (e *SystemdPodmanExecutor) start(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	ports := e.loadHostPorts(e.DataDir, task.InstanceID)
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("render quadlet on start for instance %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.chownInstanceData(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("chown instance data on start for %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.createPodmanSecrets(ctx, task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create podman secrets on start for %q: %v", task.InstanceID, err)
		return result
	}
	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd on start for instance %q: %v", task.InstanceID, err)
		return result
	}
	for _, unit := range unitNames(task) {
		if err := e.systemd().Start(ctx, unit); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("start unit %q: %v", unit, err)
			return result
		}
	}
	metadata, err := e.startMetadata(ctx, task)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}
	result.Success = true
	result.Logs = fmt.Sprintf("started instance %s", task.InstanceID)
	result.Metadata = metadata
	return result
}

func (e *SystemdPodmanExecutor) stop(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	for _, unit := range unitNames(task) {
		if err := e.systemd().Stop(ctx, unit); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("stop unit %q: %v", unit, err)
			return result
		}
	}
	result.Success = true
	result.Logs = fmt.Sprintf("stopped instance %s", task.InstanceID)
	return result
}

func (e *SystemdPodmanExecutor) resize(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	result.Metadata = resizeMetadata(task.Tier)
	if !hasValidTaskMemoryLimit(task.Tier.Memory) {
		result.Success = false
		result.Error = fmt.Sprintf("resize_app requires a valid memory limit, got %q", task.Tier.Memory)
		return result
	}
	podName := podName(task.InstanceID)
	if err := e.podman().PodExists(ctx, podName); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("instance %q pod not found: %v", task.InstanceID, err)
		return result
	}

	// Stop existing units, but retain the units that actually stopped so a
	// failed resize can restore service availability.
	stoppedUnits := make([]string, 0, len(unitNames(task)))
	for _, unit := range unitNames(task) {
		if err := e.systemd().Stop(ctx, unit); err != nil {
			for _, stopped := range stoppedUnits {
				_ = e.systemd().Start(ctx, stopped)
			}
			result.Success = false
			result.Error = fmt.Sprintf("stop unit %q for resize %q: %v", unit, task.InstanceID, err)
			return result
		}
		stoppedUnits = append(stoppedUnits, unit)
	}
	restoreOnFailure := func() string {
		var rollbackError string
		for _, unit := range stoppedUnits {
			if err := e.systemd().Start(ctx, unit); err != nil {
				rollbackError += fmt.Sprintf("; rollback start unit %q: %v", unit, err)
			}
		}
		return rollbackError
	}

	// Re-render Quadlet with updated tier limits
	ports := e.loadHostPorts(e.DataDir, task.InstanceID)
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		rollbackError := restoreOnFailure()
		result.Success = false
		result.Error = fmt.Sprintf("re-render quadlet for resize %q: %v%s", task.InstanceID, err, rollbackError)
		return result
	}

	e.writeInstanceTierInfo(e.DataDir, task.InstanceID, task.Tier)

	if err := e.chownInstanceData(task.InstanceID); err != nil {
		rollbackError := restoreOnFailure()
		result.Success = false
		result.Error = fmt.Sprintf("chown instance data on resize for %q: %v%s", task.InstanceID, err, rollbackError)
		return result
	}

	if err := e.systemd().DaemonReload(ctx); err != nil {
		rollbackError := restoreOnFailure()
		result.Success = false
		result.Error = fmt.Sprintf("daemon-reload on resize for %q: %v%s", task.InstanceID, err, rollbackError)
		return result
	}

	for _, unit := range unitNames(task) {
		if err := e.systemd().Start(ctx, unit); err != nil {
			rollbackError := restoreOnFailure()
			result.Success = false
			result.Error = fmt.Sprintf("start unit %q after resize: %v%s", unit, err, rollbackError)
			return result
		}
	}

	result.Success = true
	result.Logs = fmt.Sprintf("resized instance %s", task.InstanceID)
	return result
}
