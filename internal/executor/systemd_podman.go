package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/storage"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type SystemdPodmanExecutor struct {
	Systemd  *systemd.Manager
	Podman   *podman.Inspector
	Renderer *quadlet.Renderer
	DataDir  string
}

func NewSystemdPodman(systemdManager *systemd.Manager, podmanInspector *podman.Inspector, quadletDir, dataDir string) *SystemdPodmanExecutor {
	return &SystemdPodmanExecutor{
		Systemd:  systemdManager,
		Podman:   podmanInspector,
		Renderer: quadlet.NewRenderer(quadletDir, dataDir),
		DataDir:  dataDir,
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

	switch task.Action {
	case admiral.ActionProvisionApp:
		return e.provision(ctx, task, result)
	case admiral.ActionStartApp, admiral.ActionResumeApp:
		return e.start(ctx, task, result)
	case admiral.ActionStopApp, admiral.ActionPauseApp:
		return e.stop(ctx, task, result)
	case admiral.ActionDeprovisionApp:
		return e.deprovision(ctx, task, result)
	case admiral.ActionInspectApp:
		return e.inspect(ctx, task, result)
	case admiral.ActionBackupDatabase:
		return e.backupDatabase(ctx, task, result)
	case admiral.ActionBackupVolumes:
		return e.backupVolumes(ctx, task, result)
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
	ports, err := allocateHostPorts(e.DataDir, task.InstanceID, task.Services)
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
	if err := e.systemd().DaemonReload(ctx); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("reload systemd for instance %q: %v", task.InstanceID, err)
		return result
	}
	for _, unit := range unitNames(task) {
		if err := e.systemd().Start(ctx, unit); err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("start unit %q: %v", unit, err)
			return result
		}
	}
	writeInstanceTierInfo(e.DataDir, task.InstanceID, task.Tier)

	meta := map[string]interface{}{
		"executor":   "systemd-podman",
		"action":     "provision_app",
		"host_ports": ports,
	}
	metaBytes, _ := json.Marshal(meta)
	result.Success = true
	result.Logs = fmt.Sprintf("provisioned instance %s", task.InstanceID)
	result.Metadata = string(metaBytes)
	return result
}

func (e *SystemdPodmanExecutor) start(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	ports := loadHostPorts(e.DataDir, task.InstanceID)
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("render quadlet on start for instance %q: %v", task.InstanceID, err)
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
	result.Success = true
	result.Logs = fmt.Sprintf("started instance %s", task.InstanceID)
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

func (e *SystemdPodmanExecutor) deprovision(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	for _, unit := range unitNames(task) {
		_ = e.systemd().Stop(ctx, unit)
		_ = e.systemd().Disable(ctx, unit)
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
	if err := os.RemoveAll(instDir); err != nil {
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
	path, err := e.writeBackup(task.InstanceID, databaseType+"-database", data)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	// S3 upload is best-effort: local backup was already written successfully
	if err := e.uploadToS3(ctx, task, data); err != nil {
		result.Logs = fmt.Sprintf("database backup stored at %s, but S3 upload failed: %v", path, err)
	} else {
		result.Logs = fmt.Sprintf("%s database backup stored at %s and uploaded to S3", databaseType, path)
	}

	checksum := sha256Hex(data)
	result.Success = true
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","backup":{"backup_id":"","backup_type":"database","database_type":%q,"storage_backend":"local","storage_key":%q,"size_bytes":%d,"checksum_sha256":%q,"completed_at":%q}}`, databaseType, path, len(data), checksum, time.Now().UTC().Format(time.RFC3339))
	return result
}

func (e *SystemdPodmanExecutor) backupVolumes(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	volumes, err := e.collectVolumeTar(ctx, task)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}
	path, err := e.writeBackup(task.InstanceID, "volumes", volumes)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	// S3 upload is best-effort: local backup was already written successfully
	if err := e.uploadToS3(ctx, task, volumes); err != nil {
		result.Logs = fmt.Sprintf("volume backup stored at %s, but S3 upload failed: %v", path, err)
	} else {
		result.Logs = fmt.Sprintf("volume backup stored at %s and uploaded to S3", path)
	}

	checksum := sha256Hex(volumes)
	result.Success = true
	result.Logs = fmt.Sprintf("volume backup stored at %s", path)
	result.Metadata = fmt.Sprintf(`{"executor":"systemd-podman","backup":{"backup_id":"","backup_type":"volume","storage_backend":"local","storage_key":%q,"size_bytes":%d,"checksum_sha256":%q,"completed_at":%q}}`, path, len(volumes), checksum, time.Now().UTC().Format(time.RFC3339))
	return result
}

func (e *SystemdPodmanExecutor) deleteBackup(ctx context.Context, task admiral.FleetTask, result admiral.TaskResult) admiral.TaskResult {
	if task.Storage == nil || task.Storage.Backend == "" || task.Storage.Backend == "local" {
		if task.Storage != nil && task.Storage.Key != "" {
			// Actually delete the local file
			if err := os.Remove(task.Storage.Key); err != nil && !os.IsNotExist(err) {
				result.Success = false
				result.Error = fmt.Sprintf("delete local backup %q: %v", task.Storage.Key, err)
				return result
			}
			// Remove parent directory if empty
			parentDir := filepath.Dir(task.Storage.Key)
			_ = os.Remove(parentDir) // ignore error if not empty
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

func (e *SystemdPodmanExecutor) writeBackup(instanceID, kind string, data []byte) (string, error) {
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "backups", instanceID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.tar.gz", kind, time.Now().UTC().UnixNano()))
	file, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create backup file: %w", err)
	}
	defer file.Close()
	gw := gzip.NewWriter(file)
	defer gw.Close()
	if _, err := gw.Write(data); err != nil {
		return "", fmt.Errorf("write backup data: %w", err)
	}
	return path, nil
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
	return e.podman().Exec(ctx, containerName(task.InstanceID, svc.Name), "env", fmt.Sprintf("PGPASSWORD=%s", password), "pg_dump", "-Fc", "-U", username, databaseName)
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
	return e.podman().Exec(ctx, containerName(task.InstanceID, svc.Name), "env", fmt.Sprintf("MYSQL_PWD=%s", password), "mysqldump", "--single-transaction", "--quick", "--routines", "--events", "--triggers", "--skip-lock-tables", "-u", username, databaseName)
}

func (e *SystemdPodmanExecutor) collectVolumeTar(ctx context.Context, task admiral.FleetTask) ([]byte, error) {
	var buffer bytes.Buffer
	zw := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(zw)

	volumeServices := e.servicesWithVolumes(task)
	if len(volumeServices) == 0 {
		_ = tw.Close()
		_ = zw.Close()
		return nil, fmt.Errorf("no services with volumes for backup")
	}

	for _, svc := range volumeServices {
		volName := volumeName(task.InstanceID, svc.Name)
		inspect, err := e.podman().VolumeInspect(ctx, volName)
		if err != nil {
			_ = tw.Close()
			_ = zw.Close()
			return nil, fmt.Errorf("inspect volume %q: %w", volName, err)
		}
		mountpoint := extractMountPoint(inspect)
		if mountpoint == "" {
			_ = tw.Close()
			_ = zw.Close()
			return nil, fmt.Errorf("volume %q has no mountpoint", volName)
		}
		prefix := svc.Name + "/"
		err = filepath.Walk(mountpoint, func(path string, info os.FileInfo, err error) error {
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
			if info.Mode().IsRegular() {
				f, err := os.Open(path)
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
			_ = tw.Close()
			_ = zw.Close()
			return nil, fmt.Errorf("archive volume %q: %w", volName, err)
		}
	}
	if err := tw.Close(); err != nil {
		_ = zw.Close()
		return nil, fmt.Errorf("finalize volume archive: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize compressed volume archive: %w", err)
	}
	return buffer.Bytes(), nil
}

func (e *SystemdPodmanExecutor) servicesWithVolumes(task admiral.FleetTask) []admiral.ServiceInfo {
	var out []admiral.ServiceInfo
	for _, svc := range task.Services {
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

func (e *SystemdPodmanExecutor) systemd() *systemd.Manager {
	if e.Systemd != nil {
		return e.Systemd
	}
	return systemd.NewManager(nil)
}

func (e *SystemdPodmanExecutor) podman() *podman.Inspector {
	if e.Podman != nil {
		return e.Podman
	}
	return podman.NewInspector(nil)
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

func unitNames(task admiral.FleetTask) []string {
	if len(task.Services) > 1 || task.Tier.CPU > 0 || strings.TrimSpace(task.Tier.Memory) != "" {
		return []string{quadlet.PodUnitName(task.InstanceID)}
	}
	units := make([]string, 0, len(task.Services))
	for _, svc := range task.Services {
		units = append(units, quadlet.ContainerUnitName(task.InstanceID, svc.Name))
	}
	return units
}

func volumeName(instanceID, serviceName string) string {
	return fmt.Sprintf("admiral-%s-%s", quadlet.SafeName(instanceID), quadlet.SafeName(serviceName))
}
func sha256Hex(data []byte) string   { return fmt.Sprintf("%x", sha256Sum(data)) }
func sha256Sum(data []byte) [32]byte { return sha256.Sum256(data) }

func portsFilePath(dataDir, instanceID string) string {
	return filepath.Join(dataDir, "instances", instanceID, "ports.json")
}

func writeInstanceTierInfo(dataDir, instanceID string, tier admiral.TierInfo) {
	dir := filepath.Join(dataDir, "instances", instanceID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	info := map[string]interface{}{
		"storage": tier.Storage,
	}
	data, err := json.Marshal(info)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "tier.json"), data, 0600)
}

const minHostPort = 40000
const maxHostPort = 49999

func allocateHostPorts(dataDir, instanceID string, services []admiral.ServiceInfo) (map[string]int, error) {
	dir := filepath.Join(dataDir, "instances", instanceID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create instance dir for port allocation: %w", err)
	}
	counterFile := filepath.Join(dataDir, "next_port")
	next := minHostPort
	if data, err := os.ReadFile(counterFile); err == nil {
		fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &next)
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
	if err := os.WriteFile(counterFile, []byte(fmt.Sprintf("%d", next)), 0644); err != nil {
		return nil, fmt.Errorf("persist next port: %w", err)
	}
	portData, err := json.Marshal(ports)
	if err != nil {
		return nil, fmt.Errorf("marshal ports: %w", err)
	}
	if err := os.WriteFile(portsFilePath(dataDir, instanceID), portData, 0600); err != nil {
		return nil, fmt.Errorf("write ports file: %w", err)
	}
	return ports, nil
}

func loadHostPorts(dataDir, instanceID string) map[string]int {
	data, err := os.ReadFile(portsFilePath(dataDir, instanceID))
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
// it re-renders and starts them via systemd.
func (e *SystemdPodmanExecutor) startRestoreContainers(ctx context.Context, task admiral.FleetTask) error {
	units := unitNames(task)
	if len(units) == 0 {
		return nil
	}

	// If at least one container already exists, assume they're all running
	firstContainer := containerName(task.InstanceID, task.Services[0].Name)
	if err := e.podman().ContainerExists(ctx, firstContainer); err == nil {
		return nil
	}

	ports := loadHostPorts(e.DataDir, task.InstanceID)
	r := e.renderer()
	r.HostPorts = ports
	if err := r.Render(task); err != nil {
		return fmt.Errorf("render quadlet for restore: %w", err)
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
		path := task.Restore.StorageKey
		if _, err := os.Stat(path); err != nil {
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	path := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write S3 artifact: %w", err)
	}
	return path, nil
}

func (e *SystemdPodmanExecutor) downloadRestoreArtifact(ctx context.Context, sourceURI string) (string, error) {
	parsed, err := url.Parse(sourceURI)
	if err != nil {
		return "", fmt.Errorf("parse restore uri: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("restore uri must use https")
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	path := filepath.Join(dir, "artifact.bin")
	file, err := os.Create(path)
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
		actual, err := e.checksumArtifact(artifactPath)
		if err != nil {
			return err
		}
		if actual != task.Restore.ChecksumSHA256 {
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

	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read restore artifact: %w", err)
	}
	rawPath, err := e.expandGzipArtifact(artifactPath, data)
	if err != nil {
		return err
	}
	defer e.cleanupRestoreArtifact(rawPath)

	container := containerName(task.InstanceID, svc.Name)

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
		for i := 0; i < 15; i++ {
			out, err := e.podman().Exec(ctx, container, "mysqladmin", "ping", "-u", username, fmt.Sprintf("-p%s", password), "--silent")
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
			out, err := e.podman().Exec(ctx, container, "pg_isready", "-U", username, "-d", databaseName)
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

	switch dbEngine {
	case "mysql", "mariadb":
		cmd := fmt.Sprintf("MYSQL_PWD='%s' mysql -u %s %s < /tmp/admiral-restore.dump", password, username, databaseName)
		if _, err := e.podman().Exec(ctx, container, "/bin/sh", "-c", cmd); err != nil {
			return fmt.Errorf("run mysql restore in container %q: %w", container, err)
		}
	default:
		if _, err := e.podman().Exec(ctx, container, "env", fmt.Sprintf("PGPASSWORD=%s", password), "pg_restore", "-Fc", "-U", username, "-d", databaseName, "/tmp/admiral-restore.dump"); err != nil {
			return fmt.Errorf("run pg_restore in container %q: %w", container, err)
		}
	}
	return nil
}

func (e *SystemdPodmanExecutor) restoreVolumes(ctx context.Context, task admiral.FleetTask, artifactPath string) error {
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read restore artifact: %w", err)
	}

	targetServices := e.servicesWithVolumes(task)
	if len(targetServices) == 0 {
		return fmt.Errorf("no volume services found for restore")
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
	return nil
}

func (e *SystemdPodmanExecutor) extractGzipTarToDirFiltered(data []byte, mountpoint, prefix string) error {
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("open restore volume archive: %w", err)
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	for {
		head, err := tarReader.Next()
		if err == io.EOF {
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
			if err := os.MkdirAll(targetPath, head.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create restore directory %q: %w", targetPath, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("prepare restore path %q: %w", targetPath, err)
		}
		out, err := os.Create(targetPath)
		if err != nil {
			return fmt.Errorf("create restore file %q: %w", targetPath, err)
		}
		if _, err := io.Copy(out, tarReader); err != nil {
			out.Close()
			return fmt.Errorf("write restore file %q: %w", targetPath, err)
		}
		_ = out.Close()
	}
	return nil
}

func (e *SystemdPodmanExecutor) expandGzipArtifact(path string, data []byte) (string, error) {
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return "", fmt.Errorf("open restore archive %q: %w", path, err)
	}
	defer reader.Close()
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "restore", fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	rawPath := filepath.Join(dir, "artifact.raw")
	rawFile, err := os.Create(rawPath)
	if err != nil {
		return "", fmt.Errorf("create restore raw artifact: %w", err)
	}
	defer rawFile.Close()
	if _, err := io.Copy(rawFile, reader); err != nil {
		return "", fmt.Errorf("decompress restore artifact: %w", err)
	}
	return rawPath, nil
}

func (e *SystemdPodmanExecutor) extractGzipTarToDir(data []byte, mountpoint string) error {
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("open restore volume archive: %w", err)
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	for {
		head, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read restore volume archive: %w", err)
		}
		targetPath := filepath.Join(mountpoint, filepath.Clean(head.Name))
		if !strings.HasPrefix(targetPath, filepath.Clean(mountpoint)+string(os.PathSeparator)) && filepath.Clean(targetPath) != filepath.Clean(mountpoint) {
			return fmt.Errorf("refuse to restore path outside mountpoint: %s", head.Name)
		}
		if head.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, head.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create restore directory %q: %w", targetPath, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("prepare restore path %q: %w", targetPath, err)
		}
		out, err := os.Create(targetPath)
		if err != nil {
			return fmt.Errorf("create restore file %q: %w", targetPath, err)
		}
		if _, err := io.Copy(out, tarReader); err != nil {
			out.Close()
			return fmt.Errorf("write restore file %q: %w", targetPath, err)
		}
		_ = out.Close()
	}
	return nil
}

func (e *SystemdPodmanExecutor) checksumArtifact(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func (e *SystemdPodmanExecutor) cleanupRestoreArtifact(path string) {
	_ = os.Remove(path)
	_ = os.RemoveAll(filepath.Dir(path))
}
