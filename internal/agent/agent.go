// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
	"github.com/admiral-project/admiral/admirald/pkg/admiral/tlsconfig"
)

type Agent struct {
	NodeID                string
	APIURL                string
	FleetToken            string
	StorageCheckInterval  string
	StorageExceededAction string
	RootlessUser          string
	QuadletDir            string
	executor              executor.Executor
	http                  *http.Client
	outbox                *outbox
}

func New(nodeID, apiURL, fleetToken, caCertFile, outboxDir, storageCheckInterval, storageExceededAction, rootlessUser, quadletDir string, exec executor.Executor) (*Agent, error) {
	if err := tlsconfig.ValidateURLScheme(apiURL, "https"); err != nil {
		return nil, err
	}
	clientTLSConfig, err := tlsconfig.NewClientConfig(caCertFile)
	if err != nil {
		return nil, err
	}

	return &Agent{
		NodeID:                nodeID,
		APIURL:                apiURL,
		FleetToken:            fleetToken,
		StorageCheckInterval:  storageCheckInterval,
		StorageExceededAction: storageExceededAction,
		RootlessUser:          rootlessUser,
		QuadletDir:            quadletDir,
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

// Reconcile queries the local Podman state and reports all existing Admiral
// instances to admirald. It must be called synchronously at startup before
// consuming tasks so that admirald has an accurate view of running instances.
func (a *Agent) Reconcile(ctx context.Context) {
	a.checkAllPods(ctx)
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
	req.Header.Set("X-Admiral-Token", a.FleetToken)

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
	req.Header.Set("X-Admiral-Token", a.FleetToken)

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
