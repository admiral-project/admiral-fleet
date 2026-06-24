// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"testing"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestSimulatedExecutor_AllActions(t *testing.T) {
	exec := NewSimulated()
	ctx := context.Background()
	nodeID := "node1"

	actions := []admiral.TaskAction{
		admiral.ActionProvisionApp,
		admiral.ActionStartApp,
		admiral.ActionStopApp,
		admiral.ActionPauseApp,
		admiral.ActionResumeApp,
		admiral.ActionResizeApp,
		admiral.ActionDeprovisionApp,
		admiral.ActionBackupDatabase,
		admiral.ActionBackupVolumes,
		admiral.ActionInspectApp,
		admiral.ActionDeleteBackup,
		admiral.ActionTestStorage,
		admiral.ActionRestoreBackup,
		admiral.ActionPauseAppStorage,
		admiral.ActionReactivateApp,
	}

	for _, action := range actions {
		task := admiral.FleetTask{
			TaskID: "task1",
			NodeID: nodeID,
			Action: action,
		}
		res := exec.Execute(ctx, task, nodeID)
		if !res.Success {
			t.Errorf("action %s failed: %s", action, res.Error)
		}
	}

	// Test disallowed action
	task := admiral.FleetTask{
		TaskID: "task2",
		NodeID: nodeID,
		Action: "INVALID",
	}
	res := exec.Execute(ctx, task, nodeID)
	if res.Success {
		t.Error("expected failure for invalid action")
	}
}
