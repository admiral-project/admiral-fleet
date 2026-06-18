// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/osutil"
	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/storage"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

const (
	maxRestoreArtifactBytes = 1 << 30
	maxRestoreFileBytes     = 256 << 20
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
	if err := e.writeEncryptedSecrets(ctx, task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("write encrypted secrets for %q: %v", task.InstanceID, err)
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
					break
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
	if err := e.writeEncryptedSecrets(ctx, task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("write encrypted secrets on start for %q: %v", task.InstanceID, err)
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

	// Stop existing units
	for _, unit := range unitNames(task) {
		_ = e.systemd().Stop(ctx, unit)
	}

	// Re-render Quadlet with updated tier limits
	ports := e.loadHostPorts(e.DataDir, task.InstanceID)
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("re-render quadlet for resize %q: %v", task.InstanceID, err)
		return result
	}

	e.writeInstanceTierInfo(e.DataDir, task.InstanceID, task.Tier)

	if err := e.chownInstanceData(task.InstanceID); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("chown instance data on resize for %q: %v", task.InstanceID, err)
		return result
	}

	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("daemon-reload on resize for %q: %v", task.InstanceID, err)
		return result
	}

	for _, unit := range unitNames(task) {
		if err := e.systemd().Start(ctx, unit); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("start unit %q after resize: %v", unit, err)
			return result
		}
	}

	result.Success = true
	result.Logs = fmt.Sprintf("resized instance %s", task.InstanceID)
	return result
}

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
					break
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

func (e *SystemdPodmanExecutor) backupDatabase(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if task.Backup == nil {
		result.Success = false
		result.Error = "backup metadata is required"
		return result
	}
	backupSvc := findService(task.Services, task.Backup.Service)
	if backupSvc.Name == "" {
		result.Success = false
		result.Error = fmt.Sprintf("backup service %q not found", task.Backup.Service)
		return result
	}
	databaseType := normalizeDatabaseType(task.Backup.DatabaseType)
	data, err := e.collectDatabaseBackup(ctx, task, backupSvc, databaseType)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "backups", task.InstanceID)
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create backup dir: %v", err)
		return result
	}

	path := filepath.Join(dir, fmt.Sprintf("%s-database-%s.tar.gz", databaseType, task.OperationID))
	f, err := e.FS.Create(path)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create backup file: %v", err)
		return result
	}

	h := sha256.New()
	gw := gzip.NewWriter(io.MultiWriter(f, h))
	if _, err := gw.Write(data); err != nil {
		_ = closeAndRemove(e.FS, path, gw, f)
		result.Success = false
		result.Error = fmt.Sprintf("gzip data: %v", err)
		return result
	}
	if err := gw.Close(); err != nil {
		_ = closeAndRemove(e.FS, path, f)
		result.Success = false
		result.Error = fmt.Sprintf("close gzip: %v", err)
		return result
	}
	if err := f.Close(); err != nil {
		_ = closeAndRemove(e.FS, path)
		result.Success = false
		result.Error = fmt.Sprintf("close backup file: %v", err)
		return result
	}

	fi, _ := e.FS.Stat(path)
	checksum := fmt.Sprintf("%x", h.Sum(nil))

	backupID := ""
	if task.Storage != nil {
		backupID = task.Storage.BackupID
	}
	storageBackend := "local"
	if task.Storage != nil && task.Storage.Backend != "" && task.Storage.Backend != "local" {
		compressed, _ := e.FS.ReadFile(path)
		if err := e.uploadToS3(ctx, task, compressed); err != nil {
			result.Error = fmt.Sprintf("S3 upload failed: %v", err)
			result.Logs = fmt.Sprintf("database backup stored at %s, but S3 upload failed: %v", path, err)
			result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","backup":{"backup_id":%q,"backup_type":"database","database_type":%q,"storage_backend":"local","storage_key":%q,"size_bytes":%d,"checksum_sha256":%q,"completed_at":%q}}`, backupID, databaseType, path, fi.Size(), checksum, time.Now().UTC().Format(time.RFC3339))
			return result
		}
		storageBackend = task.Storage.Backend
		result.Logs = fmt.Sprintf("%s database backup stored at %s and uploaded to %s", databaseType, path, task.Storage.Backend)
	} else {
		result.Logs = fmt.Sprintf("%s database backup stored at %s", databaseType, path)
	}

	result.Success = true
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","backup":{"backup_id":%q,"backup_type":"database","database_type":%q,"storage_backend":%q,"storage_key":%q,"size_bytes":%d,"checksum_sha256":%q,"completed_at":%q}}`, backupID, databaseType, storageBackend, path, fi.Size(), checksum, time.Now().UTC().Format(time.RFC3339))
	return result
}

func (e *SystemdPodmanExecutor) backupVolumes(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "backups", task.InstanceID)
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create backup dir: %v", err)
		return result
	}

	path := filepath.Join(dir, fmt.Sprintf("volumes-%s.tar.gz", task.OperationID))
	f, err := e.FS.Create(path)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("create backup file: %v", err)
		return result
	}

	h := sha256.New()
	gw := gzip.NewWriter(io.MultiWriter(f, h))

	if err := e.collectVolumeTar(ctx, task, gw); err != nil {
		_ = closeAndRemove(e.FS, path, gw, f)
		result.Success = false
		result.Error = err.Error()
		return result
	}

	if err := gw.Close(); err != nil {
		_ = closeAndRemove(e.FS, path, f)
		result.Success = false
		result.Error = fmt.Sprintf("close gzip: %v", err)
		return result
	}
	if err := f.Close(); err != nil {
		_ = closeAndRemove(e.FS, path)
		result.Success = false
		result.Error = fmt.Sprintf("close backup file: %v", err)
		return result
	}

	fi, _ := e.FS.Stat(path)
	checksum := fmt.Sprintf("%x", h.Sum(nil))

	backupID := ""
	if task.Storage != nil {
		backupID = task.Storage.BackupID
	}
	storageBackend := "local"
	if task.Storage != nil && task.Storage.Backend != "" && task.Storage.Backend != "local" {
		compressed, _ := e.FS.ReadFile(path)
		if err := e.uploadToS3(ctx, task, compressed); err != nil {
			result.Error = fmt.Sprintf("S3 upload failed: %v", err)
			result.Logs = fmt.Sprintf("volume backup stored at %s, but S3 upload failed: %v", path, err)
			result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","backup":{"backup_id":%q,"backup_type":"volume","storage_backend":"local","storage_key":%q,"size_bytes":%d,"checksum_sha256":%q,"completed_at":%q}}`, backupID, path, fi.Size(), checksum, time.Now().UTC().Format(time.RFC3339))
			return result
		}
		storageBackend = task.Storage.Backend
		result.Logs = fmt.Sprintf("volume backup stored at %s and uploaded to %s", path, task.Storage.Backend)
	} else {
		result.Logs = fmt.Sprintf("volume backup stored at %s", path)
	}

	result.Success = true
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","backup":{"backup_id":%q,"backup_type":"volume","storage_backend":%q,"storage_key":%q,"size_bytes":%d,"checksum_sha256":%q,"completed_at":%q}}`, backupID, storageBackend, path, fi.Size(), checksum, time.Now().UTC().Format(time.RFC3339))
	return result
}

func (e *SystemdPodmanExecutor) deleteBackup(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if task.Storage == nil || task.Storage.Backend == "" || task.Storage.Backend == "local" {
		if task.Storage != nil && task.Storage.Key != "" {
			localPath, err := e.resolveLocalBackupPath(task.Storage.Key)
			if err != nil {
				result.Success = false
				result.Error = fmt.Sprintf("resolve local backup %q: %v", task.Storage.Key, err)
				return result
			}
			// Actually delete the local file
			if err := e.FS.Remove(localPath); err != nil && !os.IsNotExist(err) {
				result.Success = false
				result.Error = fmt.Sprintf("delete local backup %q: %v", localPath, err)
				return result
			}
			// Remove parent directory if empty
			parentDir := filepath.Dir(localPath)
			_ = e.FS.Remove(parentDir) // ignore error if not empty
		}
		result.Success = true
		result.Logs = fmt.Sprintf("backup %s deleted from local storage", task.Storage.BackupID)
		result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"delete_backup","backup_id":%q}`, task.Storage.BackupID)
		return result
	}
	s3Client, err := storage.NewS3FromConfig(task.Storage)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("init S3 client: %v", err)
		return result
	}
	if err := s3Client.DeleteObject(ctx, task.Storage.Key); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("delete from S3: %v", err)
		return result
	}
	result.Success = true
	result.Logs = fmt.Sprintf("backup %s deleted from S3 (%s/%s)", task.Storage.BackupID, task.Storage.Bucket, task.Storage.Key)
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"delete_backup","backup_id":%q}`, task.Storage.BackupID)
	return result
}

func (e *SystemdPodmanExecutor) testStorage(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if task.Storage == nil || task.Storage.Backend == "" || task.Storage.Backend == "local" {
		result.Success = true
		result.Logs = "local storage is active"
		result.Metadata = `{"executor":"systemd-podman","action":"test_backup_storage","backend":"local"}`
		return result
	}
	s3Client, err := storage.NewS3FromConfig(task.Storage)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("init S3 client: %v", err)
		return result
	}
	if err := s3Client.Test(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("S3 connectivity test failed: %v", err)
		return result
	}
	result.Success = true
	result.Logs = fmt.Sprintf("S3 storage %s bucket %q is reachable", task.Storage.Endpoint, task.Storage.Bucket)
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","action":"test_backup_storage","backend":"s3","endpoint":%q,"bucket":%q}`, task.Storage.Endpoint, task.Storage.Bucket)
	return result
}

func (e *SystemdPodmanExecutor) uploadToS3(ctx context.Context, task admiral.FleetTask, data []byte) error {
	if task.Storage == nil || task.Storage.Backend == "" || task.Storage.Backend == "local" {
		return nil
	}
	s3Client, err := storage.NewS3FromConfig(task.Storage)
	if err != nil {
		return fmt.Errorf("init S3 client: %w", err)
	}
	return s3Client.PutObject(ctx, task.Storage.Key, data)
}

func (e *SystemdPodmanExecutor) collectDatabaseBackup(ctx context.Context, task admiral.FleetTask, svc admiral.ServiceInfo, databaseType string) ([]byte, error) {
	switch databaseType {
	case "postgresql", "postgres", "postgresql16":
		return e.collectPostgresBackup(ctx, task, svc)
	case "mysql", "mariadb":
		return e.collectMySQLBackup(ctx, task, svc)
	default:
		return nil, fmt.Errorf("unsupported database backup type %q", databaseType)
	}
}

func (e *SystemdPodmanExecutor) collectPostgresBackup(ctx context.Context, task admiral.FleetTask, svc admiral.ServiceInfo) ([]byte, error) {
	databaseName, ok := lookupEnv(svc, task.Backup.DatabaseEnv)
	if !ok || strings.TrimSpace(databaseName) == "" {
		return nil, fmt.Errorf("database env %q is missing", task.Backup.DatabaseEnv)
	}
	username, ok := lookupEnv(svc, task.Backup.UsernameEnv)
	if !ok || strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("username env %q is missing", task.Backup.UsernameEnv)
	}
	password, ok := lookupEnv(svc, task.Backup.PasswordEnv)
	if !ok || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("password env %q is missing", task.Backup.PasswordEnv)
	}
	return e.podman().ExecWithEnv(ctx, executionContainerNameForService(task, svc), map[string]string{"PGPASSWORD": password}, "pg_dump", "-Fc", "-U", username, databaseName)
}

func (e *SystemdPodmanExecutor) collectMySQLBackup(ctx context.Context, task admiral.FleetTask, svc admiral.ServiceInfo) ([]byte, error) {
	databaseName, ok := lookupEnv(svc, task.Backup.DatabaseEnv)
	if !ok || strings.TrimSpace(databaseName) == "" {
		return nil, fmt.Errorf("database env %q is missing", task.Backup.DatabaseEnv)
	}
	username, ok := lookupEnv(svc, task.Backup.UsernameEnv)
	if !ok || strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("username env %q is missing", task.Backup.UsernameEnv)
	}
	password, ok := lookupEnv(svc, task.Backup.PasswordEnv)
	if !ok || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("password env %q is missing", task.Backup.PasswordEnv)
	}
	dumpCmd := "mysqldump"
	if strings.EqualFold(task.Backup.DatabaseType, "mariadb") || strings.Contains(strings.ToLower(svc.Image), "mariadb") {
		dumpCmd = "mariadb-dump"
	}
	data, err := e.podman().ExecWithEnv(ctx, executionContainerNameForService(task, svc), map[string]string{"MYSQL_PWD": password}, dumpCmd, "--single-transaction", "--quick", "--routines", "--events", "--triggers", "--skip-lock-tables", "-u", username, databaseName)
	if err == nil {
		return data, nil
	}
	if strings.Contains(err.Error(), "executable file") || strings.Contains(err.Error(), "not found") {
		var buf bytes.Buffer
		if err := e.collectVolumeTar(ctx, task, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return nil, err
}

func (e *SystemdPodmanExecutor) collectVolumeTar(ctx context.Context, task admiral.FleetTask, w io.Writer) error {
	tw := tar.NewWriter(w)
	entryCount := 0

	volumeServices := e.servicesWithVolumes(task)
	if len(volumeServices) == 0 {
		// No services define volumes — nothing to archive, not an error.
		return nil
	}

	for _, svc := range volumeServices {
		volName := volumeName(task.InstanceID, svc.Name)
		inspect, err := e.podman().VolumeInspect(ctx, volName)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", volName, err)
		}
		mountpoint := extractMountPoint(inspect)
		if mountpoint == "" {
			return fmt.Errorf("volume %q has no mountpoint", volName)
		}
		prefix := svc.Name + "/"
		err = e.FS.Walk(mountpoint, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			head, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(mountpoint, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			head.Name = prefix + rel
			if err := tw.WriteHeader(head); err != nil {
				return err
			}
			entryCount++
			if info.Mode().IsRegular() {
				f, err := e.FS.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				if _, err := io.Copy(tw, f); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("archive volume %q: %w", volName, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finalize volume archive: %w", err)
	}
	if entryCount == 0 {
		// Empty volumes are valid (fresh instance with no data yet).
		// Return nil so the backup records as succeeded, not failed.
		return nil
	}
	return nil
}

func (e *SystemdPodmanExecutor) servicesWithVolumes(task admiral.FleetTask) []admiral.ServiceInfo {
	var out []admiral.ServiceInfo
	for _, svc := range task.Services {
		if task.Backup != nil && task.Backup.Service != "" && svc.Name != task.Backup.Service {
			continue
		}
		if svc.Volume != "" {
			out = append(out, svc)
		}
	}
	return out
}

func extractMountPoint(raw []byte) string {
	var parsed []map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed) == 0 {
		return ""
	}
	if mp, ok := parsed[0]["Mountpoint"].(string); ok {
		return mp
	}
	return ""
}

func looksLikeVolumeArchive(data []byte) bool {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	_, err = tarReader.Next()
	return err == nil
}

func normalizeDatabaseType(explicit string) string {
	v := strings.ToLower(strings.TrimSpace(explicit))
	switch v {
	case "postgres", "postgresql":
		return "postgresql"
	case "mysql":
		return "mysql"
	case "mariadb":
		return "mariadb"
	default:
		return "postgresql"
	}
}

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

func deprovisionUnitNames(task admiral.FleetTask) []string {
	units := unitNames(task)
	for _, svc := range task.Services {
		if svc.Volume != "" {
			units = append(units, quadlet.VolumeUnitName(task.InstanceID, svc.Name))
		}
	}
	return units
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

// startRestoreContainers ensures all instance containers are running before
// a restore operation. If containers were removed (e.g. by pause + Quadlet cleanup),
// it re-renders and starts them via systemd. If containers exist but the pod is
// paused, it unpauses the pod so restore operations can exec into containers.
func (e *SystemdPodmanExecutor) startRestoreContainers(ctx context.Context, task admiral.FleetTask) error {
	units := unitNames(task)
	if len(units) == 0 {
		return nil
	}

	// If at least one container already exists, assume they're all running
	firstContainer := executionContainerName(task)
	if err := e.podman().ContainerExists(ctx, firstContainer); err == nil {
		// Containers exist but may be paused (frozen). Unpause if needed
		// so restore can exec into them (pg_restore, mysql, etc.).
		podName := podName(task.InstanceID)
		paused, pErr := e.podman().PodIsPaused(ctx, podName)
		if pErr != nil {
			return fmt.Errorf("check pod pause state: %w", pErr)
		}
		if paused {
			if err := e.podman().PodUnpause(ctx, podName); err != nil {
				return fmt.Errorf("unpause pod %q before restore: %w", podName, err)
			}
		}
		return nil
	}

	ports := e.loadHostPorts(e.DataDir, task.InstanceID)
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		return fmt.Errorf("render quadlet for restore: %w", err)
	}
	if err := e.chownInstanceData(task.InstanceID); err != nil {
		return fmt.Errorf("chown instance data for restore: %w", err)
	}
	if err := e.systemd().DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon-reload for restore: %w", err)
	}
	for _, unit := range units {
		if err := e.systemd().Start(ctx, unit); err != nil {
			return fmt.Errorf("start container %q for restore: %w", unit, err)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) restoreBackup(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if task.Restore == nil {
		result.Success = false
		result.Error = "restore metadata is required"
		return result
	}
	if strings.TrimSpace(task.Restore.BackupID) == "" {
		result.Success = false
		result.Error = "backup_id is required"
		return result
	}
	if strings.TrimSpace(task.Restore.StorageKey) == "" {
		result.Success = false
		result.Error = "restore source uri or storage key is required"
		return result
	}

	artifactPath, err := e.fetchRestoreArtifact(ctx, task)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	// Ensure containers are running before restore (pause may have stopped them)
	if err := e.startRestoreContainers(ctx, task); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	if err := e.applyRestoreArtifact(ctx, task, artifactPath); err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.Logs = fmt.Sprintf("restored backup %s for instance %s", task.Restore.BackupID, task.InstanceID)
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","restore":{"backup_id":%q,"artifact":%q}}`, task.Restore.BackupID, artifactPath)
	return result
}

func (e *SystemdPodmanExecutor) fetchRestoreArtifact(ctx context.Context, task admiral.FleetTask) (string, error) {
	switch strings.ToLower(strings.TrimSpace(task.Restore.StorageBackend)) {
	case "local", "local_path", "":
		path, err := e.resolveLocalBackupPath(task.Restore.StorageKey)
		if err != nil {
			return "", fmt.Errorf("resolve local restore artifact %q: %w", task.Restore.StorageKey, err)
		}
		if _, err := e.FS.Stat(path); err != nil {
			return "", fmt.Errorf("local backup artifact %q not accessible: %w", path, err)
		}
		return path, nil
	case "https":
		return e.downloadRestoreArtifact(ctx, task.Restore.StorageKey)
	case "s3":
		return e.downloadS3Artifact(ctx, task)
	default:
		return "", fmt.Errorf("restore source type %q is not supported yet", task.Restore.StorageBackend)
	}
}

func (e *SystemdPodmanExecutor) localBackupRoot() string {
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	return filepath.Clean(filepath.Join(base, "backups"))
}

func (e *SystemdPodmanExecutor) resolveLocalBackupPath(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("backup key is required")
	}

	root := e.localBackupRoot()
	cleanRoot := filepath.Clean(root)
	var candidate string
	if filepath.IsAbs(key) {
		candidate = filepath.Clean(key)
	} else {
		candidate = filepath.Clean(filepath.Join(cleanRoot, key))
	}

	rel, err := filepath.Rel(cleanRoot, candidate)
	if err != nil {
		return "", fmt.Errorf("compute relative backup path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes backup root %q", candidate, cleanRoot)
	}
	return candidate, nil
}

func (e *SystemdPodmanExecutor) downloadS3Artifact(ctx context.Context, task admiral.FleetTask) (string, error) {
	s3Client, err := storage.NewS3FromConfig(task.Storage)
	if err != nil {
		return "", fmt.Errorf("init S3 client for restore: %w", err)
	}
	data, err := s3Client.GetObject(ctx, task.Restore.StorageKey)
	if err != nil {
		return "", fmt.Errorf("download from S3: %w", err)
	}
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "restore", fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	path := filepath.Join(dir, "artifact.bin")
	if err := e.FS.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write S3 artifact: %w", err)
	}
	return path, nil
}

func isPrivateHost(host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	// Strip port if present
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	// Try direct IP parsing first
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("refuse connection to private IP %q", ip)
		}
		return nil
	}
	// Resolve DNS and check all returned IPs
	ips, err := net.DefaultResolver.LookupHost(context.Background(), h)
	if err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", h, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("refuse connection to host %q resolving to private IP %q", h, ip)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) downloadRestoreArtifact(ctx context.Context, sourceURI string) (string, error) {
	parsed, err := url.Parse(sourceURI)
	if err != nil {
		return "", fmt.Errorf("parse restore uri: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("restore uri must use https")
	}
	if err := isPrivateHost(parsed.Host); err != nil {
		return "", fmt.Errorf("restore uri rejected: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURI, nil)
	if err != nil {
		return "", fmt.Errorf("create restore download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download restore artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download restore artifact: http %d", resp.StatusCode)
	}

	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "restore", fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	if err := e.FS.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	path := filepath.Join(dir, "artifact.bin")
	file, err := e.FS.Create(path)
	if err != nil {
		return "", fmt.Errorf("create restore artifact file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", fmt.Errorf("save restore artifact: %w", err)
	}
	return path, nil
}

func (e *SystemdPodmanExecutor) applyRestoreArtifact(ctx context.Context, task admiral.FleetTask, artifactPath string) error {
	if task.Restore.VerifyChecksum && strings.TrimSpace(task.Restore.ChecksumSHA256) != "" {
		matched, actual, err := e.verifyRestoreChecksum(artifactPath, task.Restore.ChecksumSHA256)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("checksum mismatch: want %s got %s", task.Restore.ChecksumSHA256, actual)
		}
	}

	switch task.Restore.BackupType {
	case "database":
		return e.restoreDatabase(ctx, task, artifactPath)
	case "volume":
		return e.restoreVolumes(ctx, task, artifactPath)
	default:
		return fmt.Errorf("restore backup type %q is not supported", task.Restore.BackupType)
	}
}

func (e *SystemdPodmanExecutor) restoreDatabase(ctx context.Context, task admiral.FleetTask, artifactPath string) error {
	if task.Restore.Service == "" {
		return fmt.Errorf("restore service is required")
	}
	svc := findService(task.Services, task.Restore.Service)
	if svc.Name == "" {
		return fmt.Errorf("restore service %q not found", task.Restore.Service)
	}
	databaseName, ok := lookupEnv(svc, task.Backup.DatabaseEnv)
	if !ok || strings.TrimSpace(databaseName) == "" {
		return fmt.Errorf("database env %q is missing", task.Backup.DatabaseEnv)
	}
	username, ok := lookupEnv(svc, task.Backup.UsernameEnv)
	if !ok || strings.TrimSpace(username) == "" {
		return fmt.Errorf("username env %q is missing", task.Backup.UsernameEnv)
	}
	password, ok := lookupEnv(svc, task.Backup.PasswordEnv)
	if !ok || strings.TrimSpace(password) == "" {
		return fmt.Errorf("password env %q is missing", task.Backup.PasswordEnv)
	}

	data, err := e.FS.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read restore artifact: %w", err)
	}
	if looksLikeVolumeArchive(data) {
		return e.restoreVolumes(ctx, task, artifactPath)
	}
	rawPath, err := e.expandGzipArtifact(artifactPath, data)
	if err != nil {
		return err
	}
	defer e.cleanupRestoreArtifact(rawPath)

	container := executionContainerNameForService(task, svc)

	var existsErr error
	for i := 0; i < 15; i++ {
		if err := e.podman().ContainerExists(ctx, container); err == nil {
			existsErr = nil
			break
		}
		existsErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if existsErr != nil {
		return fmt.Errorf("container %q never became available: %w", container, existsErr)
	}

	dbEngine := normalizeDatabaseType(task.Restore.DatabaseType)
	if dbEngine == "mysql" || dbEngine == "mariadb" {
		pingCmd := "mysqladmin"
		if dbEngine == "mariadb" || strings.Contains(strings.ToLower(svc.Image), "mariadb") {
			pingCmd = "mariadb-admin"
		}
		for i := 0; i < 15; i++ {
			out, err := e.podman().ExecWithEnv(ctx, container, map[string]string{"MYSQL_PWD": password}, pingCmd, "ping", "-u", username, "--silent")
			if err == nil && strings.TrimSpace(string(out)) == "mysqld is alive" {
				break
			}
			if i == 14 {
				return fmt.Errorf("database in container %q not ready after 30s: %w", container, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	if dbEngine == "postgresql" {
		for i := 0; i < 15; i++ {
			out, err := e.podman().ExecWithEnv(ctx, container, map[string]string{"PGPASSWORD": password}, "pg_isready", "-U", username, "-d", databaseName)
			if err == nil && strings.Contains(string(out), "accepting connections") {
				break
			}
			if i == 14 {
				return fmt.Errorf("database in container %q not ready after 30s: %w", container, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	if _, err := e.podman().CopyToContainer(ctx, rawPath, container+":/tmp/admiral-restore.dump"); err != nil {
		return fmt.Errorf("copy restore artifact into container %q: %w", container, err)
	}

	dumpData, err := e.FS.ReadFile(rawPath) // #nosec G304 -- rawPath is generated in a controlled restore staging directory
	if err != nil {
		return fmt.Errorf("read decompressed dump for restore: %w", err)
	}

	switch dbEngine {
	case "mysql", "mariadb":
		restoreCmd := "mysql"
		if dbEngine == "mariadb" || strings.Contains(strings.ToLower(svc.Image), "mariadb") {
			restoreCmd = "mariadb"
		}
		if _, err := e.podman().ExecWithStdin(ctx, container, map[string]string{"MYSQL_PWD": password}, bytes.NewReader(dumpData), restoreCmd, "-u", username, databaseName); err != nil {
			return fmt.Errorf("run %s restore in container %q: %w", restoreCmd, container, err)
		}
	default:
		if _, err := e.podman().ExecWithEnv(ctx, container, map[string]string{"PGPASSWORD": password}, "pg_restore", "--clean", "--if-exists", "--no-owner", "--no-privileges", "-Fc", "-U", username, "-d", databaseName, "/tmp/admiral-restore.dump"); err != nil {
			return fmt.Errorf("run pg_restore in container %q: %w", container, err)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) restoreVolumes(ctx context.Context, task admiral.FleetTask, artifactPath string) error {
	data, err := e.FS.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read restore artifact: %w", err)
	}

	targetServices := e.servicesWithVolumes(task)
	if len(targetServices) == 0 {
		return fmt.Errorf("no volume services found for restore")
	}

	// Stop containers that own the target volumes before restoring data.
	// Raw database files (InnoDB, etc.) must not be overwritten while the
	// server process has them open — doing so corrupts the data dictionary.
	for _, svc := range targetServices {
		unitName := quadlet.ContainerUnitName(task.InstanceID, svc.Name)
		if err := e.systemd().Stop(ctx, unitName); err != nil {
			return fmt.Errorf("stop container %q before volume restore: %w", unitName, err)
		}
	}

	for _, svc := range targetServices {
		volName := volumeName(task.InstanceID, svc.Name)
		inspect, err := e.podman().VolumeInspect(ctx, volName)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", volName, err)
		}
		mountpoint := extractMountPoint(inspect)
		if mountpoint == "" {
			return fmt.Errorf("volume %q has no mountpoint", volName)
		}
		prefix := svc.Name + "/"
		if err := e.extractGzipTarToDirFiltered(data, mountpoint, prefix); err != nil {
			return fmt.Errorf("restore volume %q: %w", volName, err)
		}
	}

	// Restart containers after restore so the database process runs crash
	// recovery and picks up the replaced data files.
	for _, svc := range targetServices {
		unitName := quadlet.ContainerUnitName(task.InstanceID, svc.Name)
		if err := e.systemd().Start(ctx, unitName); err != nil {
			return fmt.Errorf("start container %q after volume restore: %w", unitName, err)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) extractGzipTarToDirFiltered(data []byte, mountpoint, prefix string) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open restore volume archive: %w", err)
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	for {
		head, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read restore volume archive: %w", err)
		}
		if !strings.HasPrefix(head.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(head.Name, prefix)
		targetPath := filepath.Join(mountpoint, filepath.Clean(rel))
		if !strings.HasPrefix(targetPath, filepath.Clean(mountpoint)+string(os.PathSeparator)) && filepath.Clean(targetPath) != filepath.Clean(mountpoint) {
			return fmt.Errorf("refuse to restore path outside mountpoint: %s", rel)
		}
		if head.FileInfo().IsDir() {
			if err := e.FS.MkdirAll(targetPath, head.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create restore directory %q: %w", targetPath, err)
			}
			continue
		}
		if err := e.FS.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("prepare restore path %q: %w", targetPath, err)
		}
		out, err := e.FS.Create(targetPath)
		if err != nil {
			return fmt.Errorf("create restore file %q: %w", targetPath, err)
		}
		if err := copyWithLimit(out, tarReader, maxRestoreFileBytes, "restore file"); err != nil {
			_ = out.Close()
			return fmt.Errorf("write restore file %q: %w", targetPath, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("close restore file %q: %w", targetPath, err)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) expandGzipArtifact(path string, data []byte) (string, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("open restore archive %q: %w", path, err)
	}
	defer reader.Close()
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "restore", fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	if err := e.FS.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	e.chownRestoreDir(dir)
	rawPath := filepath.Join(dir, "artifact.raw")
	rawFile, err := e.FS.Create(rawPath)
	if err != nil {
		return "", fmt.Errorf("create restore raw artifact: %w", err)
	}
	defer rawFile.Close()
	if err := copyWithLimit(rawFile, reader, maxRestoreArtifactBytes, "restore artifact"); err != nil {
		return "", fmt.Errorf("decompress restore artifact: %w", err)
	}
	return rawPath, nil
}

func (e *SystemdPodmanExecutor) checksumArtifact(path string) (string, error) {
	data, err := e.FS.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func (e *SystemdPodmanExecutor) verifyRestoreChecksum(path, expected string) (bool, string, error) {
	actual, err := e.checksumArtifact(path)
	if err != nil {
		return false, "", err
	}
	if checksumMatches(expected, actual) {
		return true, actual, nil
	}

	data, err := e.FS.ReadFile(path)
	if err != nil {
		return false, actual, err
	}
	payload, err := gunzipBytes(data)
	if err != nil {
		return false, actual, err
	}
	sum := sha256.Sum256(payload)
	payloadSum := fmt.Sprintf("sha256:%x", sum[:])
	return checksumMatches(expected, payloadSum), payloadSum, nil
}

func checksumMatches(expected, actual string) bool {
	e := strings.TrimSpace(expected)
	a := strings.TrimSpace(actual)
	e = strings.TrimPrefix(e, "sha256:")
	a = strings.TrimPrefix(a, "sha256:")
	return e == a
}

func gunzipBytes(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (e *SystemdPodmanExecutor) cleanupRestoreArtifact(path string) {
	_ = e.FS.Remove(path)
	_ = e.FS.RemoveAll(filepath.Dir(path))
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
