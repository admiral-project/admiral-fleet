// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

const (
	helperActionBackup  = "backup"
	helperActionRestore = "restore"
	defaultHelperBinary = "admiral-fleet-backup"
)

// prepareStorageRoots ensures the data-plane directories under DataDir are
// owned by the rootless user so admiral-fleet-backup can create and remove
// backup artifacts and restore staging files without root privileges.
func (e *SystemdPodmanExecutor) prepareStorageRoots() error {
	if strings.TrimSpace(e.RootlessUser) == "" {
		return nil
	}
	u, err := e.UserLookup.Lookup(e.RootlessUser)
	if err != nil {
		return fmt.Errorf("lookup rootless user %q: %w", e.RootlessUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("parse rootless uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("parse rootless gid %q: %w", u.Gid, err)
	}
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	for _, root := range []string{
		filepath.Join(base, "backups"),
		filepath.Join(base, "restore"),
		filepath.Join(base, "tmp"),
	} {
		if err := e.FS.MkdirAll(root, 0751); err != nil {
			return fmt.Errorf("create storage root %q: %w", root, err)
		}
		if err := e.FS.Chown(root, uid, gid); err != nil {
			return fmt.Errorf("chown storage root %q: %w", root, err)
		}
		if err := e.FS.Chmod(root, 0751); err != nil {
			return fmt.Errorf("chmod storage root %q: %w", root, err)
		}
	}
	return nil
}

// delegateDataTask hands the data-plane work to admiral-fleet-backup running
// as the rootless user. The task (including credentials) travels over stdin,
// never through argv. The helper answers with a TaskResult on stdout.
func (e *SystemdPodmanExecutor) delegateDataTask(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult, action string) admiral.TaskResult {
	if err := e.prepareStorageRoots(); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}
	payload, err := json.Marshal(task)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("serialize %s task for helper: %v", action, err)
		return result
	}
	out, err := e.runHelper(ctx, action, payload)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("%s helper failed: %v", action, err)
		return result
	}
	var helperResult admiral.TaskResult
	if err := json.Unmarshal(out, &helperResult); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("parse %s helper result: %v", action, err)
		return result
	}
	result.Success = helperResult.Success
	result.Error = helperResult.Error
	result.Logs = helperResult.Logs
	result.Metadata = helperResult.Metadata
	return result
}

func (e *SystemdPodmanExecutor) runHelper(ctx context.Context, action string, payload []byte) ([]byte, error) {
	if strings.TrimSpace(e.RootlessUser) == "" {
		return nil, fmt.Errorf("rootless user is required to run the data helper")
	}
	u, err := e.UserLookup.Lookup(e.RootlessUser)
	if err != nil {
		return nil, fmt.Errorf("lookup rootless user %q: %w", e.RootlessUser, err)
	}
	runner := podman.CommandRunner{}
	args, err := e.helperCommandArgs(u.Uid, action)
	if err != nil {
		return nil, err
	}
	return runner.RunWithStdin(ctx, bytes.NewReader(payload), "runuser", args...)
}

// helperCommandArgs builds the runuser argv that spawns admiral-fleet-backup
// as the rootless user. The rootless uid is passed so XDG_RUNTIME_DIR can be
// computed without leaking the user name into validation.
func (e *SystemdPodmanExecutor) helperCommandArgs(rootlessUID, action string) ([]string, error) {
	if strings.TrimSpace(action) == "" {
		return nil, fmt.Errorf("helper action is required")
	}
	xdgRuntimeDir := filepath.Join("/run/user", rootlessUID)
	binary := e.helperBinaryPath()
	return []string{
		"-u", e.RootlessUser, "--",
		"env",
		"XDG_RUNTIME_DIR=" + xdgRuntimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(xdgRuntimeDir, "bus"),
		"ADMIRAL_FLEET_DATA_DIR=" + e.DataDir,
		"ADMIRAL_FLEET_ROOTLESS_USER=" + e.RootlessUser,
		binary, action,
	}, nil
}

func (e *SystemdPodmanExecutor) helperBinaryPath() string {
	binary := e.HelperBinary
	if strings.TrimSpace(binary) == "" {
		binary = defaultHelperBinary
	}
	if !filepath.IsAbs(binary) {
		for _, candidate := range []string{binary, filepath.Join("/usr/bin", binary)} {
			if resolved, err := exec.LookPath(candidate); err == nil {
				return resolved
			}
		}
		return filepath.Join("/usr/bin", binary)
	}
	return binary
}
