// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/osutil"
	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type SystemdPodmanExecutor struct {
	Systemd      *systemd.Manager
	Podman       *podman.Inspector
	Renderer     *quadlet.Renderer
	FS           osutil.FileSystem
	UserLookup   osutil.UserLookup
	DataDir      string
	RootlessUser string // empty = rootful; set = rootless systemd --user target
}

func NewSystemdPodman(systemdManager *systemd.Manager, podmanInspector *podman.Inspector, quadletDir, dataDir, rootlessUser string) *SystemdPodmanExecutor {
	return NewSystemdPodmanWithFS(systemdManager, podmanInspector, quadletDir, dataDir, rootlessUser, osutil.RealFileSystem{}, osutil.RealUserLookup{})
}

func closeAndRemove(fs osutil.FileSystem, path string, closers ...io.Closer) error {
	var cleanupErrs []error
	for _, closer := range closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	if err := fs.Remove(path); err != nil && !os.IsNotExist(err) {
		cleanupErrs = append(cleanupErrs, err)
	}
	return errors.Join(cleanupErrs...)
}

func copyWithLimit(dst io.Writer, src io.Reader, limit int64, label string) error {
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return fmt.Errorf("%s exceeds maximum size of %d bytes", label, limit)
	}
	return nil
}

func parsePublishedPort(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("empty published port")
	}
	if port, err := strconv.Atoi(value); err == nil {
		return port, nil
	}

	lastColon := strings.LastIndex(value, ":")
	if lastColon == -1 || lastColon == len(value)-1 {
		return 0, fmt.Errorf("unsupported podman port output %q", raw)
	}
	port, err := strconv.Atoi(strings.TrimSpace(value[lastColon+1:]))
	if err != nil {
		return 0, fmt.Errorf("parse published port %q: %w", raw, err)
	}
	return port, nil
}

func NewSystemdPodmanWithFS(systemdManager *systemd.Manager, podmanInspector *podman.Inspector, quadletDir, dataDir, rootlessUser string, fs osutil.FileSystem, userLookup osutil.UserLookup) *SystemdPodmanExecutor {
	rd := quadlet.NewRenderer(quadletDir, dataDir)
	// Ensure data dir is traversable for rootless user
	if rootlessUser != "" {
		for _, dir := range []string{dataDir, filepath.Join(dataDir, "instances")} {
			if err := fs.MkdirAll(dir, 0751); err != nil {
				break
			}
			_ = fs.Chmod(dir, 0751)
		}
	}

	return &SystemdPodmanExecutor{
		Systemd:      systemdManager,
		Podman:       podmanInspector,
		Renderer:     rd,
		FS:           fs,
		UserLookup:   userLookup,
		DataDir:      dataDir,
		RootlessUser: rootlessUser,
	}
}

func (e *SystemdPodmanExecutor) Execute(ctx context.Context, task admiral.FleetTask, nodeID string) admiral.TaskResult {
	result := admiral.TaskResult{TaskID: task.TaskID, OperationID: task.OperationID, NodeID: nodeID}
	if task.NodeID != nodeID {
		result.Success = false
		result.Error = fmt.Sprintf("task node_id %q does not match fleet node_id %q", task.NodeID, nodeID)
		return result
	}
	if !isAllowedAction(task.Action) {
		result.Success = false
		result.Error = fmt.Sprintf("unsupported action %q", task.Action)
		return result
	}
	if strings.TrimSpace(e.RootlessUser) == "" {
		result.Success = false
		result.Error = "rootlessUser is required: Admiral requires rootless workloads"
		return result
	}

	switch task.Action {
	case admiral.ActionProvisionApp:
		return e.provision(ctx, task, result)
	case admiral.ActionStartApp, admiral.ActionReactivateApp, admiral.ActionResumeApp:
		return e.start(ctx, task, result)
	case admiral.ActionStopApp, admiral.ActionPauseAppStorage, admiral.ActionPauseApp:
		return e.stop(ctx, task, result)
	case admiral.ActionDeprovisionApp:
		return e.deprovision(ctx, task, result)
	case admiral.ActionInspectApp:
		return e.inspect(ctx, task, result)
	case admiral.ActionBackupDatabase:
		return e.backupDatabase(ctx, task, result)
	case admiral.ActionBackupVolumes:
		return e.backupVolumes(ctx, task, result)
	case admiral.ActionResizeApp:
		return e.resize(ctx, task, result)
	case admiral.ActionDeleteBackup:
		return e.deleteBackup(ctx, task, result)
	case admiral.ActionTestStorage:
		return e.testStorage(ctx, task, result)
	case admiral.ActionRestoreBackup:
		return e.restoreBackup(ctx, task, result)
	default:
		result.Success = false
		result.Error = fmt.Sprintf("systemd-podman executor action %q is not implemented yet", task.Action)
		result.Metadata = `{"executor":"systemd-podman"}`
		return result
	}
}
