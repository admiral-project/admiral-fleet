// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

// admiral-fleet-backup runs the data plane of backup and restore operations
// as the rootless workload user. It receives the full task over stdin (so
// credentials never reach the command line) and answers with a TaskResult on
// stdout. The fleet supervises the helper and owns container lifecycle.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admiral-fleet-backup <backup|restore>")
		os.Exit(2)
	}
	action := strings.TrimSpace(os.Args[1])
	if action != "backup" && action != "restore" {
		fmt.Fprintf(os.Stderr, "unsupported action %q\n", action)
		os.Exit(2)
	}

	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		slog.Error("read task payload", "error", err)
		os.Exit(1)
	}
	var task admiral.FleetTask
	if err := json.Unmarshal(payload, &task); err != nil {
		slog.Error("parse task payload", "error", err)
		os.Exit(1)
	}
	if strings.TrimSpace(task.TaskID) == "" {
		slog.Error("task payload missing task_id")
		os.Exit(1)
	}

	exec := buildExecutor()
	result := exec.Execute(context.Background(), task, task.NodeID)

	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		slog.Error("encode task result", "error", err)
		os.Exit(1)
	}
	if !result.Success {
		if result.Error != "" {
			fmt.Fprintln(os.Stderr, result.Error)
		}
		os.Exit(1)
	}
}

var _ executor.Executor = (*executor.SystemdPodmanExecutor)(nil)
