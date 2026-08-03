// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admiral-fleet/internal/storage"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

const (
	maxRestoreArtifactBytes = 1 << 30
	maxRestoreFileBytes     = 256 << 20
)

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

	// The fleet orchestrates the container lifecycle (re-render/start after
	// pause) and then hands the data-plane restore to the helper running as
	// the rootless user. The helper downloads the artifact, verifies the
	// checksum and applies it with its own credentials.
	if e.DelegateRestore {
		if err := e.startRestoreContainers(ctx, task); err != nil {
			result.Success = false
			result.Error = err.Error()
			return result
		}
		return e.delegateDataTask(ctx, task, result, helperActionRestore)
	}

	artifactPath, err := e.fetchRestoreArtifact(ctx, task)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	// Ensure containers are running before restore (pause may have stopped them)
	if !e.RestoreContainersReady {
		if err := e.startRestoreContainers(ctx, task); err != nil {
			result.Success = false
			result.Error = err.Error()
			return result
		}
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
	data, err := s3Client.GetObjectLimited(ctx, task.Restore.StorageKey, maxRestoreArtifactBytes)
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
	if err := e.writeFileNoFollow(path, data, 0600); err != nil {
		return "", fmt.Errorf("write S3 artifact: %w", err)
	}
	return path, nil
}

func isRestrictedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return true
	}
	restrictedCIDRs := []string{
		"100.64.0.0/10", // CGNAT
		"192.0.2.0/24",  // Documentation (TEST-NET-1)
		"2001:db8::/32", // Documentation (IPv6)
	}
	for _, cidr := range restrictedCIDRs {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

func resolveRestoreHost(ctx context.Context, host string) ([]net.IP, error) {
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	// Strip port if present
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	// Try direct IP parsing first
	if ip := net.ParseIP(h); ip != nil {
		if isRestrictedIP(ip) {
			return nil, fmt.Errorf("refuse connection to private or restricted IP %q", ip)
		}
		return []net.IP{ip}, nil
	}
	// Resolve all answers once and reject if any answer is restricted.
	addrInfos, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve host %q: %w", h, err)
	}
	if len(addrInfos) == 0 {
		return nil, fmt.Errorf("host %q has no addresses", h)
	}
	ips := make([]net.IP, 0, len(addrInfos))
	for _, addr := range addrInfos {
		ip := net.IP(addr.AsSlice())
		if isRestrictedIP(ip) {
			return nil, fmt.Errorf("refuse connection to host %q resolving to private or restricted IP %q", h, ip)
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func isPrivateHost(host string) error {
	_, err := resolveRestoreHost(context.Background(), host)
	return err
}

func newRestoreHTTPClient(ctx context.Context, sourceURI string) (*http.Client, error) {
	parsed, err := url.Parse(sourceURI)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("restore uri must use https")
	}
	initial, err := resolveRestoreHost(ctx, parsed.Host)
	if err != nil {
		return nil, err
	}
	var mu sync.RWMutex
	pinned := map[string][]net.IP{strings.ToLower(parsed.Hostname()): initial}
	pinURL := func(target *url.URL) error {
		if target.Scheme != "https" {
			return fmt.Errorf("restore redirect must use https")
		}
		ips, err := resolveRestoreHost(ctx, target.Host)
		if err != nil {
			return err
		}
		mu.Lock()
		pinned[strings.ToLower(target.Hostname())] = ips
		mu.Unlock()
		return nil
	}
	transport := &http.Transport{
		DialContext: func(dialCtx context.Context, _, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			mu.RLock()
			ips := pinned[strings.ToLower(strings.TrimSuffix(host, "."))]
			mu.RUnlock()
			if len(ips) == 0 {
				return nil, fmt.Errorf("host %q was not pinned after validation", host)
			}
			var lastErr error
			for _, ip := range ips {
				conn, dialErr := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return pinURL(req.URL)
	}
	return client, nil
}

func (e *SystemdPodmanExecutor) downloadRestoreArtifact(ctx context.Context, sourceURI string) (string, error) {
	parsed, err := url.Parse(sourceURI)
	if err != nil {
		return "", fmt.Errorf("parse restore uri: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("restore uri must use https")
	}
	client, err := newRestoreHTTPClient(ctx, sourceURI)
	if err != nil {
		return "", fmt.Errorf("restore uri rejected: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURI, nil)
	if err != nil {
		return "", fmt.Errorf("create restore download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download restore artifact: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
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
	file, err := e.FS.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", fmt.Errorf("create restore artifact file: %w", err)
	}
	defer file.Close()
	if err := copyWithLimit(file, resp.Body, maxRestoreArtifactBytes, "restore artifact"); err != nil {
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

	volumeArchive, err := e.looksLikeVolumeArchiveFile(artifactPath)
	if err != nil {
		return fmt.Errorf("inspect restore artifact: %w", err)
	}
	if volumeArchive {
		return e.restoreVolumes(ctx, task, artifactPath)
	}
	rawPath, err := e.expandGzipArtifactFromFile(artifactPath)
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
			out, err := e.podman().ExecWithEnv(ctx, container, map[string]string{"MYSQL_PWD": password}, pingCmd, "-h", "127.0.0.1", "ping", "-u", username, "--silent")
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

	switch dbEngine {
	case "mysql", "mariadb":
		restoreCmd := "mysql"
		if dbEngine == "mariadb" || strings.Contains(strings.ToLower(svc.Image), "mariadb") {
			restoreCmd = "mariadb"
		}
		dumpFile, err := e.FS.Open(rawPath)
		if err != nil {
			return fmt.Errorf("open decompressed dump for restore: %w", err)
		}
		defer dumpFile.Close()
		if _, err := e.podman().ExecWithStdin(ctx, container, map[string]string{"MYSQL_PWD": password}, dumpFile, restoreCmd, "-h", "127.0.0.1", "-u", username, databaseName); err != nil {
			return fmt.Errorf("run %s restore in container %q: %w", restoreCmd, container, err)
		}
	default:
		if _, err := e.podman().ExecWithEnv(ctx, container, map[string]string{"PGPASSWORD": password}, "pg_restore", "--clean", "--if-exists", "--no-owner", "--no-privileges", "-Fc", "-h", "127.0.0.1", "-U", username, "-d", databaseName, "/tmp/admiral-restore.dump"); err != nil {
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
	volumeTargets := e.volumeTargets(task)
	if len(volumeTargets) == 0 {
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
		if err := e.extractGzipTarToDirFiltered(ctx, data, mountpoint, prefix); err != nil {
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

func (e *SystemdPodmanExecutor) extractGzipTarToDirFiltered(ctx context.Context, data []byte, mountpoint, prefix string) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open restore volume archive: %w", err)
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	var filtered bytes.Buffer
	tarWriter := tar.NewWriter(&filtered)
	idMap, err := e.podman().UserNamespaceIDMap(ctx)
	if err != nil {
		return err
	}
	gidMap, err := e.podman().UserNamespaceGIDMap(ctx)
	if err != nil {
		return err
	}
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
		rel = filepath.Clean(rel)
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("refuse to restore path outside mountpoint: %s", rel)
		}
		if head.Typeflag != tar.TypeDir && head.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported restore entry type %d for %q", head.Typeflag, rel)
		}
		if head.Typeflag == tar.TypeReg && head.Size > maxRestoreFileBytes {
			return fmt.Errorf("restore file %q exceeds maximum size of %d bytes", rel, maxRestoreFileBytes)
		}
		var mapped bool
		var mappedID uint64
		for _, entry := range idMap {
			if mappedID, mapped = entry.HostToContainer(uint64(head.Uid)); mapped {
				break
			}
		}
		if !mapped {
			return fmt.Errorf("restore file %q has unmapped uid %d", rel, head.Uid)
		}
		head.Uid = int(mappedID)
		mapped = false
		for _, entry := range gidMap {
			if mappedID, mapped = entry.HostToContainer(uint64(head.Gid)); mapped {
				break
			}
		}
		if !mapped {
			return fmt.Errorf("restore file %q has unmapped gid %d", rel, head.Gid)
		}
		head.Gid = int(mappedID)
		head.Name = filepath.ToSlash(rel)
		if err := tarWriter.WriteHeader(head); err != nil {
			return fmt.Errorf("write filtered restore entry %q: %w", rel, err)
		}
		if head.Typeflag == tar.TypeReg {
			if _, err := io.CopyN(tarWriter, tarReader, head.Size); err != nil {
				return fmt.Errorf("copy restore file %q: %w", rel, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("finalize filtered restore archive: %w", err)
	}
	if err := e.podman().ExtractTar(ctx, bytes.NewReader(filtered.Bytes()), mountpoint); err != nil {
		return fmt.Errorf("extract filtered restore archive: %w", err)
	}
	return nil
}

func (e *SystemdPodmanExecutor) looksLikeVolumeArchiveFile(path string) (bool, error) {
	file, err := e.FS.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return false, nil
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	_, err = tarReader.Next()
	return err == nil, nil
}

func (e *SystemdPodmanExecutor) expandGzipArtifactFromFile(path string) (string, error) {
	input, err := e.FS.Open(path)
	if err != nil {
		return "", fmt.Errorf("open restore archive %q: %w", path, err)
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return "", fmt.Errorf("open restore archive %q: %w", path, err)
	}
	defer reader.Close()
	base := e.DataDir
	if strings.TrimSpace(base) == "" {
		base = "/var/lib/admiral"
	}
	dir := filepath.Join(base, "restore", fmt.Sprintf("%d", time.Now().UTC().UnixNano()))
	if err := e.FS.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("create restore staging dir: %w", err)
	}
	rawPath := filepath.Join(dir, "artifact.raw")
	rawFile, err := e.FS.Create(rawPath)
	if err != nil {
		return "", fmt.Errorf("create restore raw artifact: %w", err)
	}
	defer rawFile.Close()
	if err := copyWithLimit(rawFile, reader, maxRestoreArtifactBytes, "restore artifact"); err != nil {
		return "", fmt.Errorf("decompress restore artifact: %w", err)
	}
	if err := e.chownRestoreDir(dir); err != nil {
		return "", err
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
