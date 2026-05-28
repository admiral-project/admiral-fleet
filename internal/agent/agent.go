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
	NodeID      string
	APIURL      string
	SharedToken string
	executor    executor.Executor
	http        *http.Client
}

func New(nodeID, apiURL, sharedToken, caCertFile string, exec executor.Executor) (*Agent, error) {
	if err := tlsconfig.ValidateURLScheme(apiURL, "https"); err != nil {
		return nil, err
	}
	clientTLSConfig, err := tlsconfig.NewClientConfig(caCertFile)
	if err != nil {
		return nil, err
	}

	return &Agent{
		NodeID:      nodeID,
		APIURL:      apiURL,
		SharedToken: sharedToken,
		executor:    exec,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: clientTLSConfig,
			},
		},
	}, nil
}

func (a *Agent) HandleTask(task admiral.FleetTask) error {
	exec := a.executor
	if exec == nil {
		exec = executor.NewSimulated()
	}
	result := exec.Execute(context.Background(), task, a.NodeID)
	return a.Report(result)
}

func (a *Agent) Report(result admiral.TaskResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode task result: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, a.APIURL+"/api/v1/fleet/callback", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admiral-Token", a.SharedToken)

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
