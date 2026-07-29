// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"testing"

	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestSimulatedExecutorSucceedsForKnownAction(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(context.Background(), admiral.FleetTask{
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
	res := exec.Execute(context.Background(), admiral.FleetTask{NodeID: "node_2", Action: admiral.ActionProvisionApp}, "node_1")
	if res.Success {
		t.Fatal("expected wrong node to fail")
	}
}

func TestSimulatedExecutorRejectsUnknownAction(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(context.Background(), admiral.FleetTask{NodeID: "node_1", Action: admiral.TaskAction("bad_action")}, "node_1")
	if res.Success {
		t.Fatal("expected unknown action to fail")
	}
}

func TestPublishedHealthcheckAddressUsesPublishAddress(t *testing.T) {
	exec := &SystemdPodmanExecutor{
		Renderer: &quadlet.Renderer{PublishAddress: "10.99.0.2"},
	}
	if got := exec.publishedHealthcheckAddress(40000); got != "10.99.0.2:40000" {
		t.Fatalf("published healthcheck address = %q, want %q", got, "10.99.0.2:40000")
	}
}

func TestPublishedHealthcheckAddressDefaultsToLoopback(t *testing.T) {
	exec := &SystemdPodmanExecutor{
		Renderer: &quadlet.Renderer{},
	}
	if got := exec.publishedHealthcheckAddress(40000); got != "127.0.0.1:40000" {
		t.Fatalf("published healthcheck address = %q, want %q", got, "127.0.0.1:40000")
	}
}
