// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
	"github.com/admiral-project/admiral/admirald/pkg/admiral/tlsconfig"
)

var ErrNoTaskAvailable = errors.New("no task available")

type Agent struct {
	NodeID                string
	APIURL                string
	FleetToken            string
	StorageCheckInterval  string
	StorageExceededAction string
	RootlessUser          string
	QuadletDir            string
	taskPublicKey         ed25519.PublicKey
	executor              executor.Executor
	http                  *http.Client
	outbox                *outbox
}

func New(nodeID, apiURL, fleetToken, caCertFile, outboxDir, storageCheckInterval, storageExceededAction, rootlessUser, quadletDir, taskPublicKeyHex string, exec executor.Executor) (*Agent, error) {
	if err := tlsconfig.ValidateURLScheme(apiURL, "https"); err != nil {
		return nil, err
	}
	clientTLSConfig, err := tlsconfig.NewClientConfig(caCertFile)
	if err != nil {
		return nil, err
	}

	var taskPublicKey ed25519.PublicKey
	if taskPublicKeyHex != "" {
		keyBytes, err := hex.DecodeString(taskPublicKeyHex)
		if err != nil {
			return nil, fmt.Errorf("parse task public key: %w", err)
		}
		taskPublicKey = ed25519.PublicKey(keyBytes)
	}

	return &Agent{
		NodeID:                nodeID,
		APIURL:                apiURL,
		FleetToken:            fleetToken,
		StorageCheckInterval:  storageCheckInterval,
		StorageExceededAction: storageExceededAction,
		RootlessUser:          rootlessUser,
		QuadletDir:            quadletDir,
		taskPublicKey:         taskPublicKey,
		executor:              exec,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: clientTLSConfig,
			},
		},
		outbox: newOutbox(outboxDir),
	}, nil
}

// ClaimTask claims the next available task from admirald via HTTP API.
func (a *Agent) ClaimTask() (*admiral.FleetTask, string, error) {
	body, err := json.Marshal(map[string]string{"node_id": a.NodeID})
	if err != nil {
		return nil, "", fmt.Errorf("encode claim request: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.APIURL+"/api/v1/fleet/tasks/claim", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("create claim request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("claim task: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, "", ErrNoTaskAvailable
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("claim task returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		CommandID    string             `json:"command_id"`
		Task         *admiral.FleetTask `json:"task"`
		AttemptCount int                `json:"attempt_count"`
		MaxAttempts  int                `json:"max_attempts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", fmt.Errorf("decode claim response: %w", err)
	}
	if result.CommandID == "" || result.Task == nil {
		return nil, "", fmt.Errorf("invalid claim response: missing command_id or task")
	}

	if a.taskPublicKey != nil && result.Task != nil && result.Task.TaskSignature != "" {
		if err := verifyTaskSignature(result.Task, a.taskPublicKey); err != nil {
			return nil, "", fmt.Errorf("task verification failed: %w", err)
		}
	}

	return result.Task, result.CommandID, nil
}

func verifyTaskSignature(task *admiral.FleetTask, publicKey ed25519.PublicKey) error {
	sig, err := hex.DecodeString(task.TaskSignature)
	if err != nil {
		return fmt.Errorf("decode task signature: %w", err)
	}

	verifyTask := *task
	verifyTask.TaskSignature = ""
	verifyTask.SignedAt = 0
	payload, err := json.Marshal(verifyTask)
	if err != nil {
		return fmt.Errorf("marshal task for verification: %w", err)
	}

	signedAt := task.SignedAt
	msg := append(payload, []byte(fmt.Sprintf("%d", signedAt))...)
	if !ed25519.Verify(publicKey, msg, sig) {
		return fmt.Errorf("task signature verification failed")
	}
	return nil
}

// ReportRunning notifies admirald that a claimed task has started execution.
func (a *Agent) ReportRunning(commandID string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.APIURL+"/api/v1/fleet/tasks/"+commandID+"/running", nil)
	if err != nil {
		return fmt.Errorf("create running request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("report running: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("report running returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// RenewLease extends the lease on a running command.
func (a *Agent) RenewLease(commandID string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.APIURL+"/api/v1/fleet/tasks/"+commandID+"/renew-lease", nil)
	if err != nil {
		return fmt.Errorf("create renew-lease request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("renew lease returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// StartLeaseRenewer periodically extends the lease for a running command.
// Returns a stop function to cancel the renewal goroutine.
func (a *Agent) StartLeaseRenewer(commandID string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(90 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.RenewLease(commandID); err != nil {
					slog.Error("lease renewal failed", "command_id", commandID, "error", err)
				}
			}
		}
	}()
	return cancel
}

// SendResult posts a task result to the admirald callback endpoint.
func (a *Agent) SendResult(result admiral.TaskResult) error {
	return a.send(result)
}

// Reconcile queries the local Podman state and reports all existing Admiral
// instances to admirald. It must be called synchronously at startup before
// consuming tasks so that admirald has an accurate view of running instances.
func (a *Agent) Reconcile(ctx context.Context) {
	a.checkAllPods(ctx)
}

// StartReconciler triggers Reconcile at a regular interval. This ensures
// the control plane view stays in sync with the node even if some async
// health reports are lost.
func (a *Agent) StartReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.Reconcile(ctx)
		}
	}
}

func (a *Agent) HandleTask(task admiral.FleetTask) error {
	if a.outbox != nil {
		_ = a.outbox.flush(a.send)
	}
	exec := a.executor
	if exec == nil {
		exec = executor.NewSimulated()
	}
	result := exec.Execute(context.Background(), task, a.NodeID)
	if err := a.send(result); err != nil {
		if a.outbox != nil {
			_ = a.outbox.enqueue(result)
		}
		return err
	}
	if a.outbox != nil {
		_ = a.outbox.flush(a.send)
	}
	return nil
}

func (a *Agent) postStorage(report admiral.StorageReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode storage report: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.APIURL+"/api/v1/fleet/storage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create storage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("send storage report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("storage report failed with HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *Agent) StartOutboxFlusher(ctx context.Context, interval time.Duration) {
	if a.outbox == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			_ = a.outbox.flush(a.send)
		}
	}
}

// StartBackupStorageWarner logs a visible warning periodically if no external
// backup storage is configured. This ensures operators are aware that data
// is only protected by local snapshots which may be lost with the node.
func (a *Agent) StartBackupStorageWarner(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Initial check after some delay
	go func() {
		time.Sleep(1 * time.Minute)
		a.warnIfNoBackupStorage()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.warnIfNoBackupStorage()
		}
	}
}

func (a *Agent) warnIfNoBackupStorage() {
	// Rootless check: Ensure we're not running as root.
	if os.Getuid() == 0 {
		slog.Error("SECURITY VIOLATION: admiral-fleet must not run as root. Rootless execution is required.")
	}

	// Backup storage check:
	// Simple heuristic: if we don't have S3 credentials, we're likely only doing local backups.
	accessKey := os.Getenv("ADMIRAL_S3_ACCESS_KEY_ID")
	secretKey := os.Getenv("ADMIRAL_S3_SECRET_ACCESS_KEY")

	if accessKey == "" || secretKey == "" {
		slog.Warn("SECURITY WARNING: No remote backup storage (S3) configured. Backups are stored LOCALLY only and will be lost if this node fails.")
	}
}

// FetchTaskEncryptionKey retrieves the shared AES-256-GCM task encryption key
// from admirald over the network. The request is authenticated with the
// per-node fleet token.
//
// This is a network fallback for environments where the key cannot be
// distributed via environment variable. Prefer setting
// ADMIRAL_TASK_ENCRYPTION_KEY in the fleet environment to avoid sending
// the key over the network.
func (a *Agent) FetchTaskEncryptionKey() (string, error) {
	slog.Warn("fetching task encryption key over network; prefer ADMIRAL_TASK_ENCRYPTION_KEY in local environment")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.APIURL+"/api/v1/nodes/task-encryption-key", nil)
	if err != nil {
		return "", fmt.Errorf("create task-encryption-key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch task-encryption-key: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("task-encryption-key returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		TaskEncryptionKey string `json:"task_encryption_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode task-encryption-key response: %w", err)
	}
	if result.TaskEncryptionKey == "" {
		return "", fmt.Errorf("task-encryption-key response is empty")
	}
	return result.TaskEncryptionKey, nil
}

func (a *Agent) send(result admiral.TaskResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode task result: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.APIURL+"/api/v1/fleet/callback", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("send callback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback failed with HTTP %d", resp.StatusCode)
	}
	return nil
}
