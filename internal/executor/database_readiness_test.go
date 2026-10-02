// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitForDatabaseRetriesUntilReady(t *testing.T) {
	calls := 0
	err := waitForDatabase(context.Background(), "demo-db", time.Second, time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("waitForDatabase returned an error after readiness converged: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected three readiness probes, got %d", calls)
	}
}

func TestWaitForDatabaseReturnsBoundedReadinessError(t *testing.T) {
	started := time.Now()
	err := waitForDatabase(context.Background(), "demo-db", 20*time.Millisecond, time.Millisecond, func(context.Context) error {
		return errors.New("connection refused")
	})
	if err == nil || !strings.Contains(err.Error(), `database in container "demo-db" not ready`) {
		t.Fatalf("expected actionable readiness timeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("readiness wait exceeded its bound: %s", elapsed)
	}
}

func TestWaitForDatabaseHonorsTaskCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := waitForDatabase(ctx, "demo-db", time.Second, time.Millisecond, func(context.Context) error {
		cancel()
		return errors.New("connection refused")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected task cancellation, got %v", err)
	}
}
