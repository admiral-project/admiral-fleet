// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
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
	ImagePullInterval     string
	RootlessUser          string
	QuadletDir            string
	taskPublicKey         ed25519.PublicKey
	executor              executor.Executor
	http                  *http.Client
	outbox                *outbox
	replayMu              sync.Mutex
	seenTasks             map[string]time.Time
	podmanRunner          podman.Runner
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
		ImagePullInterval:     envOrDefault("ADMIRAL_FLEET_IMAGE_PULL_INTERVAL", "1h"),
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
		outbox:    newOutbox(outboxDir),
		seenTasks: make(map[string]time.Time),
		podmanRunner: podman.RemoteRunner{
			RootlessUser: rootlessUser,
			DataDir:      envOrDefault("ADMIRAL_FLEET_DATA_DIR", "/var/lib/admiral"),
		},
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// ClaimTask claims the next available task from admirald via HTTP API.
func (a *Agent) ClaimTask() (*admiral.FleetTask, string, error) {
	return a.ClaimTaskContext(context.Background())
}

// ClaimTaskContext claims a task and stops promptly when ctx is cancelled.
func (a *Agent) ClaimTaskContext(ctx context.Context) (*admiral.FleetTask, string, error) {
	body, err := json.Marshal(map[string]string{"node_id": a.NodeID})
	if err != nil {
		return nil, "", fmt.Errorf("encode claim request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.APIURL+"/api/v1/fleet/tasks/claim", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("create claim request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)
	mac := hmac.New(sha256.New, []byte(a.FleetToken))
	_, _ = mac.Write(body)
	req.Header.Set("X-Admiral-Task-Signature", hex.EncodeToString(mac.Sum(nil)))
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("claim task: %w", err)
	}
	defer drainAndCloseResponse(resp)

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

	if result.Task.TaskSignature == "" {
		slog.Error("SECURITY: refusing unsigned fleet task", "task_id", result.Task.TaskID, "command_id", result.CommandID)
		return nil, "", fmt.Errorf("task signature is required but missing")
	}
	if a.taskPublicKey == nil {
		slog.Error("SECURITY: refusing signed fleet task without verification key", "task_id", result.Task.TaskID, "command_id", result.CommandID)
		return nil, "", fmt.Errorf("task public key is not configured")
	}
	if err := verifyTaskSignature(result.Task, a.taskPublicKey); err != nil {
		return nil, "", fmt.Errorf("task verification failed: %w", err)
	}
	if err := a.rememberTask(result.Task); err != nil {
		return nil, "", err
	}

	return result.Task, result.CommandID, nil
}

func verifyTaskSignature(task *admiral.FleetTask, publicKey ed25519.PublicKey) error {
	if task.SignedAt == 0 {
		return fmt.Errorf("task signature timestamp is missing")
	}
	const signatureWindow = 15 * time.Minute
	signedAtTime := time.Unix(task.SignedAt, 0)
	if delta := time.Since(signedAtTime); delta > signatureWindow || delta < -signatureWindow {
		return fmt.Errorf("task signature timestamp is outside the %s window", signatureWindow)
	}
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

func (a *Agent) rememberTask(task *admiral.FleetTask) error {
	a.replayMu.Lock()
	defer a.replayMu.Unlock()
	now := time.Now()
	for id, seenAt := range a.seenTasks {
		if now.Sub(seenAt) > 15*time.Minute {
			delete(a.seenTasks, id)
		}
	}
	if _, ok := a.seenTasks[task.TaskID]; ok {
		return fmt.Errorf("task %q has already been accepted", task.TaskID)
	}
	a.seenTasks[task.TaskID] = now
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
	defer drainAndCloseResponse(resp)
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
	defer drainAndCloseResponse(resp)
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

// FlushOutbox attempts to deliver all persisted task results before shutdown.
func (a *Agent) FlushOutbox() error {
	if a.outbox == nil {
		return nil
	}
	return a.outbox.flush(a.send)
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
	return a.HandleTaskContext(context.Background(), task)
}

// HandleTaskContext executes one task and persists a retryable result if the
// callback cannot be delivered. Cancellation is passed to the executor.
func (a *Agent) HandleTaskContext(ctx context.Context, task admiral.FleetTask) error {
	if a.outbox != nil {
		if err := a.outbox.flush(a.send); err != nil {
			slog.Warn("failed to flush task result outbox", "error", err)
		}
	}
	exec := a.executor
	if exec == nil {
		exec = executor.NewSimulated()
	}
	result := exec.Execute(ctx, task, a.NodeID)
	if err := a.send(result); err != nil {
		if a.outbox != nil {
			if enqueueErr := a.outbox.enqueue(result); enqueueErr != nil {
				slog.Error("failed to persist task result in outbox", "task_id", task.TaskID, "error", enqueueErr)
			}
		}
		return err
	}
	if a.outbox != nil {
		if err := a.outbox.flush(a.send); err != nil {
			slog.Warn("failed to flush task result outbox", "error", err)
		}
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
	defer drainAndCloseResponse(resp)

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
		timer := time.NewTimer(1 * time.Minute)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			a.warnIfNoBackupStorage()
		}
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
	// Backup storage check:
	// Simple heuristic: if we don't have S3 credentials, we're likely only doing local backups.
	accessKey := os.Getenv("ADMIRAL_S3_ACCESS_KEY_ID")
	secretKey := os.Getenv("ADMIRAL_S3_SECRET_ACCESS_KEY")

	if accessKey == "" || secretKey == "" {
		slog.Warn("SECURITY WARNING: No remote backup storage (S3) configured. Backups are stored LOCALLY only and will be lost if this node fails.")
	}
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
	mac := hmac.New(sha256.New, []byte(a.FleetToken))
	_, _ = mac.Write(body)
	req.Header.Set("X-Admiral-Task-Signature", hex.EncodeToString(mac.Sum(nil)))

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("send callback: %w", err)
	}
	defer drainAndCloseResponse(resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback failed with HTTP %d", resp.StatusCode)
	}
	return nil
}

func drainAndCloseResponse(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
