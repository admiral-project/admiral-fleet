// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	databaseReadinessTimeout  = 30 * time.Second
	databaseReadinessInterval = 2 * time.Second
)

var errDatabaseReadinessProbeUnavailable = errors.New("database readiness probe command is unavailable")

func waitForDatabase(ctx context.Context, container string, timeout, interval time.Duration, probe func(context.Context) error) error {
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		if err := probeContext.Err(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if lastErr == nil {
				lastErr = err
			}
			return fmt.Errorf("database in container %q not ready after %s: %w", container, timeout, lastErr)
		}

		if err := probe(probeContext); err == nil {
			if err := probeContext.Err(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("database in container %q not ready after %s: %w", container, timeout, err)
			}
			return nil
		} else {
			if errors.Is(err, errDatabaseReadinessProbeUnavailable) {
				return err
			}
			lastErr = err
		}

		timer := time.NewTimer(interval)
		select {
		case <-probeContext.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("database in container %q not ready after %s: %w", container, timeout, lastErr)
		case <-timer.C:
		}
	}
}

func (e *SystemdPodmanExecutor) waitDatabaseReady(
	ctx context.Context,
	databaseType string,
	container string,
	image string,
	username string,
	password string,
	databaseName string,
) error {
	dbEngine := normalizeDatabaseType(databaseType)
	var probe func(context.Context) error

	switch dbEngine {
	case "mysql", "mariadb":
		pingCommand := "mysqladmin"
		if dbEngine == "mariadb" || strings.Contains(strings.ToLower(image), "mariadb") {
			pingCommand = "mariadb-admin"
		}
		probe = func(probeContext context.Context) error {
			output, err := e.podman().ExecWithEnv(
				probeContext,
				container,
				map[string]string{"MYSQL_PWD": password},
				pingCommand,
				"-h", "127.0.0.1", "ping", "-u", username, "--silent",
			)
			if err != nil {
				if strings.Contains(err.Error(), "executable file") || strings.Contains(err.Error(), "not found") {
					return fmt.Errorf("%w: %v", errDatabaseReadinessProbeUnavailable, err)
				}
				return err
			}
			if strings.TrimSpace(string(output)) != "mysqld is alive" {
				return fmt.Errorf("database ping returned %q", strings.TrimSpace(string(output)))
			}
			return nil
		}
	case "postgresql":
		probe = func(probeContext context.Context) error {
			output, err := e.podman().ExecWithEnv(
				probeContext,
				container,
				map[string]string{"PGPASSWORD": password},
				"pg_isready", "-U", username, "-d", databaseName,
			)
			if err != nil {
				if strings.Contains(err.Error(), "executable file") || strings.Contains(err.Error(), "not found") {
					return fmt.Errorf("%w: %v", errDatabaseReadinessProbeUnavailable, err)
				}
				return err
			}
			if !strings.Contains(string(output), "accepting connections") {
				return fmt.Errorf("database readiness check returned %q", strings.TrimSpace(string(output)))
			}
			return nil
		}
	default:
		return fmt.Errorf("unsupported database type %q", databaseType)
	}

	err := waitForDatabase(ctx, container, databaseReadinessTimeout, databaseReadinessInterval, probe)
	if errors.Is(err, errDatabaseReadinessProbeUnavailable) {
		return fmt.Errorf("database readiness probe unavailable in container %q: %w", container, err)
	}
	return err
}
