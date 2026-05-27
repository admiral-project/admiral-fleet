package executor

import (
	"testing"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestSimulatedExecutorSucceedsForKnownAction(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionProvisionApp,
		InstanceID:  "inst_1",
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if res.OperationID != "op_1" || res.TaskID != "task_1" {
		t.Fatalf("result did not preserve identifiers: %+v", res)
	}
}

func TestSimulatedExecutorRejectsWrongNode(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(admiral.FleetTask{NodeID: "node_2", Action: admiral.ActionProvisionApp}, "node_1")
	if res.Success {
		t.Fatal("expected wrong node to fail")
	}
}

func TestSimulatedExecutorRejectsUnknownAction(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(admiral.FleetTask{NodeID: "node_1", Action: admiral.TaskAction("bad_action")}, "node_1")
	if res.Success {
		t.Fatal("expected unknown action to fail")
	}
}
