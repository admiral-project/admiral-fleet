// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type CommandRunner struct{}

func (r CommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := security.ValidateExecParams(name, args); err != nil {
		return nil, err
	}
	sanitizedArgs := security.SanitizeArgs(args)
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name and args are validated by security.ValidateExecParams
	cmd.Dir = "/tmp"
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			sanitizedStderr := security.Sanitize(stderr.String())
			return out, fmt.Errorf("%s %v: %w: %s", name, sanitizedArgs, err, sanitizedStderr)
		}
		return out, fmt.Errorf("%s %v: %w", name, sanitizedArgs, err)
	}
	return out, nil
}

type Manager struct {
	Runner    Runner
	Timeout   time.Duration
	RunAsUser string // empty = rootful systemd; set = rootless user systemd --user
}

func NewManager(runner Runner) *Manager {
	return &Manager{
		Runner:  runner,
		Timeout: 30 * time.Second,
	}
}

func (m *Manager) DaemonReload(ctx context.Context) error {
	if m.RunAsUser != "" {
		return m.rootlessDaemonReload(ctx)
	}
	_, err := m.run(ctx, "daemon-reload")
	return err
}

func (m *Manager) Start(ctx context.Context, unit string) error {
	_, err := m.run(ctx, "start", unit)
	return err
}

func (m *Manager) Stop(ctx context.Context, unit string) error {
	_, err := m.run(ctx, "stop", unit)
	return err
}

func (m *Manager) Restart(ctx context.Context, unit string) error {
	_, err := m.run(ctx, "restart", unit)
	return err
}

func (m *Manager) Enable(ctx context.Context, unit string) error {
	_, err := m.run(ctx, "enable", unit)
	return err
}

func (m *Manager) Disable(ctx context.Context, unit string) error {
	_, err := m.run(ctx, "disable", unit)
	return err
}

func (m *Manager) ResetFailed(ctx context.Context) error {
	_, err := m.run(ctx, "reset-failed")
	return err
}

func (m *Manager) Status(ctx context.Context, unit string) ([]byte, error) {
	return m.run(ctx, "status", "--no-pager", unit)
}

func (m *Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runner := m.Runner
	if runner == nil {
		runner = CommandRunner{}
	}

	if m.RunAsUser != "" {
		return m.runAsUser(runCtx, runner, args...)
	}

	name := "systemctl"
	cmdArgs := args

	return runner.Run(runCtx, name, cmdArgs...)
}

func (m *Manager) runAsUser(ctx context.Context, runner Runner, args ...string) ([]byte, error) {
	systemctlArgs := append([]string{"systemctl", "--machine=" + m.RunAsUser + "@", "--user"}, args...)
	cmdArgs := append([]string{"--wait", "--collect", "--working-directory=/tmp"}, systemctlArgs...)
	return runner.Run(ctx, "systemd-run", cmdArgs...)
}

func (m *Manager) rootlessDaemonReload(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		_, err := m.run(ctx, "daemon-reload")
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientRootlessReloadError(err) || attempt == 5 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return lastErr
}

func isTransientRootlessReloadError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Connection reset by peer") ||
		strings.Contains(msg, "Transport endpoint is not connected")
}
