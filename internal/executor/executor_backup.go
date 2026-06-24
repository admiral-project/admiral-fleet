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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/storage"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

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

	volumeTargets := e.volumeTargets(task)
	if len(volumeTargets) == 0 {
		// No services define volumes — nothing to archive, not an error.
		return nil
	}

	for _, target := range volumeTargets {
		volName := target.volumeName
		inspect, err := e.podman().VolumeInspect(ctx, volName)
		if err != nil {
			return fmt.Errorf("inspect volume %q: %w", volName, err)
		}
		mountpoint := extractMountPoint(inspect)
		if mountpoint == "" {
			return fmt.Errorf("volume %q has no mountpoint", volName)
		}
		prefix := target.archivePrefix + "/"
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
		if svc.Volume != "" || len(svc.SharedVolumes) > 0 {
			out = append(out, svc)
		}
	}
	return out
}

type volumeArchiveTarget struct {
	volumeName    string
	archivePrefix string
}

func (e *SystemdPodmanExecutor) volumeTargets(task admiral.FleetTask) []volumeArchiveTarget {
	targets := make([]volumeArchiveTarget, 0)
	for _, svc := range e.servicesWithVolumes(task) {
		if svc.Volume != "" {
			targets = append(targets, volumeArchiveTarget{
				volumeName:    volumeName(task.InstanceID, svc.Name),
				archivePrefix: svc.Name,
			})
		}
		for _, shared := range svc.SharedVolumes {
			targets = append(targets, volumeArchiveTarget{
				volumeName:    sharedVolumeName(task.InstanceID, shared.Name),
				archivePrefix: svc.Name + "__shared__" + shared.Name,
			})
		}
	}
	return targets
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
