package executor

import (
	"context"
	"fmt"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type Executor interface {
	Execute(ctx context.Context, task admiral.FleetTask, nodeID string) admiral.TaskResult
}

type SimulatedExecutor struct{}

func NewSimulated() *SimulatedExecutor {
	return &SimulatedExecutor{}
}

func (e *SimulatedExecutor) Execute(ctx context.Context, task admiral.FleetTask, nodeID string) admiral.TaskResult {
	result := admiral.TaskResult{
		TaskID:      task.TaskID,
		OperationID: task.OperationID,
		NodeID:      nodeID,
	}

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

	result.Success = true
	result.Logs = fmt.Sprintf("simulated %s completed", task.Action)
	result.Metadata = `{"executor":"simulated"}`
	return result
}

func isAllowedAction(action admiral.TaskAction) bool {
	switch action {
	case admiral.ActionProvisionApp,
		admiral.ActionStartApp,
		admiral.ActionStopApp,
		admiral.ActionPauseApp,
		admiral.ActionResumeApp,
		admiral.ActionResizeApp,
		admiral.ActionDeprovisionApp,
		admiral.ActionBackupDatabase:
		return true
	default:
		return false
	}
}
