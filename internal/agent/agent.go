package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type Agent struct {
	NodeID      string
	APIURL      string
	SharedToken string
	executor    *executor.SimulatedExecutor
	http        *http.Client
}

func New(nodeID, apiURL, sharedToken string) *Agent {
	return &Agent{
		NodeID:      nodeID,
		APIURL:      apiURL,
		SharedToken: sharedToken,
		executor:    executor.NewSimulated(),
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (a *Agent) HandleTask(task admiral.FleetTask) error {
	result := a.executor.Execute(task, a.NodeID)
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
