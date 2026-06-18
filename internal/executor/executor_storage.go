// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"

	"github.com/admiral-project/admiral/admiral-fleet/internal/storage"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

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
