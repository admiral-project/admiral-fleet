// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func lookupEnv(svc admiral.ServiceInfo, key string) (string, bool) {
	if val, ok := svc.Env[key]; ok {
		return val, true
	}
	if val, ok := svc.Secrets[key]; ok {
		return val, true
	}
	return "", false
}

func findService(services []admiral.ServiceInfo, name string) admiral.ServiceInfo {
	for _, svc := range services {
		if svc.Name == name {
			return svc
		}
	}
	return admiral.ServiceInfo{}
}

func resizeMetadata(tier admiral.TierInfo) string {
	payload, err := json.Marshal(map[string]interface{}{
		"action":      admiral.ActionResizeApp,
		"target_tier": tier,
	})
	if err != nil {
		return ""
	}
	return string(payload)
}

func hasValidTaskMemoryLimit(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return false
	}
	units := []string{"kib", "kb", "ki", "k", "mib", "mb", "mi", "m", "gib", "gb", "gi", "g", "tib", "tb", "ti", "t"}
	for _, unit := range units {
		if strings.HasSuffix(value, unit) {
			num := strings.TrimSpace(value[:len(value)-len(unit)])
			if num == "" {
				return false
			}
			if _, err := strconv.ParseFloat(num, 64); err != nil {
				return false
			}
			return true
		}
	}
	return false
}

func (e *SystemdPodmanExecutor) chownInstanceData(instanceID string) error {
	if e.RootlessUser == "" {
		return nil
	}
	u, err := e.UserLookup.Lookup(e.RootlessUser)
	if err != nil {
		return fmt.Errorf("lookup rootless user %q: %w", e.RootlessUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	dataDir := e.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/var/lib/admiral"
	}
	instDir := filepath.Join(dataDir, "instances", instanceID)
	if err := e.FS.Walk(instDir, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return e.FS.Chown(path, uid, gid)
	}); err != nil {
		return err
	}
	// Ensure rootless user can traverse to the instance env files
	for _, dir := range []string{dataDir, filepath.Join(dataDir, "instances")} {
		if err := e.FS.Chmod(dir, 0751); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("chmod %q for rootless traversal: %w", dir, err)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) chownRestoreDir(dir string) {
	if e.RootlessUser == "" {
		return
	}
	u, err := e.UserLookup.Lookup(e.RootlessUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = e.FS.Chown(dir, uid, gid)
}

func (e *SystemdPodmanExecutor) systemd() *systemd.Manager {
	if e.Systemd != nil {
		return e.Systemd
	}
	m := systemd.NewManager(nil)
	m.RunAsUser = e.RootlessUser
	return m
}

func (e *SystemdPodmanExecutor) podman() *podman.Inspector {
	if e.Podman != nil {
		e.Podman.RootlessUser = e.RootlessUser
		return e.Podman
	}
	insp := podman.NewInspector(nil)
	insp.RootlessUser = e.RootlessUser
	return insp
}

func (e *SystemdPodmanExecutor) renderer() *quadlet.Renderer {
	if e.Renderer != nil {
		return e.Renderer
	}
	return quadlet.NewRenderer("", "")
}

func containerName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", quadlet.SafeName(instanceID), quadlet.SafeName(serviceName))
}

func executionContainerName(task admiral.FleetTask) string {
	return containerName(task.InstanceID, "infra")
}

func executionContainerNameForService(task admiral.FleetTask, svc admiral.ServiceInfo) string {
	return containerName(task.InstanceID, svc.Name)
}

func usesPod(_ admiral.FleetTask) bool {
	return true
}

func unitNames(task admiral.FleetTask) []string {
	return []string{quadlet.PodUnitName(task.InstanceID)}
}

func volumeName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", quadlet.SafeName(instanceID), quadlet.SafeName(serviceName))
}

func podName(instanceID string) string {
	return fmt.Sprintf("admiral-%s", quadlet.SafeName(instanceID))
}
func portsFilePath(dataDir, instanceID string) string {
	return filepath.Join(dataDir, "instances", instanceID, "ports.json")
}

func (e *SystemdPodmanExecutor) writeEncryptedSecrets(ctx context.Context, task admiral.FleetTask) error {
	dataDir := e.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/var/lib/admiral"
	}

	for _, svc := range task.Services {
		if len(svc.Secrets) == 0 {
			continue
		}
		credDir := quadlet.CredFilePathPrefix(dataDir, task.InstanceID, svc.Name)
		if err := e.FS.MkdirAll(credDir, 0700); err != nil {
			return fmt.Errorf("create cred dir for %q/%q: %w", task.InstanceID, svc.Name, err)
		}
		for k, v := range svc.Secrets {
			path := credDir + "-" + quadlet.SafeName(k) + ".cred"
			if err := systemd.EncryptCred(ctx, nil, k, strings.NewReader(v), path); err != nil {
				return fmt.Errorf("encrypt secret %q for %q/%q: %w", k, task.InstanceID, svc.Name, err)
			}
		}
	}
	return nil
}
