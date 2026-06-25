// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type stdinRunner interface {
	RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
}

type CommandRunner struct{}

func (r CommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, nil, name, args...)
}

func (r CommandRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, stdin, name, args...)
}

func (r CommandRunner) runWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	if err := security.ValidateExecParams(name, args); err != nil {
		return nil, err
	}
	sanitizedArgs := security.SanitizeArgs(args)
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name and args are validated by security.ValidateExecParams
	cmd.Dir = "/tmp"
	cmd.Stdin = stdin
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
	Runner     Runner
	Timeout    time.Duration
	RunAsUser  string // empty = rootful systemd; set = rootless user systemd --user
	lookupUser func(string) (*user.User, error)
}

func NewManager(runner Runner) *Manager {
	return &Manager{
		Runner:     runner,
		Timeout:    30 * time.Second,
		lookupUser: user.Lookup,
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
	if m.RunAsUser != "" {
		return m.startRootless(ctx, unit)
	}
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

func (m *Manager) startRootless(ctx context.Context, unit string) error {
	_, err := m.run(ctx, "start", unit)
	if err == nil {
		return nil
	}
	if !isMissingRootlessUnitError(err) {
		return err
	}
	if err := m.DaemonReload(ctx); err != nil {
		return fmt.Errorf("reload rootless manager before starting %q: %w", unit, err)
	}
	_, err = m.run(ctx, "start", unit)
	if err != nil {
		return err
	}
	return nil
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
	if err := m.ensureLingerEnabled(ctx, runner); err != nil {
		return nil, err
	}
	return m.runAsUserCommand(ctx, runner, args...)
}

func (m *Manager) runAsUserCommand(ctx context.Context, runner Runner, args ...string) ([]byte, error) {
	// EL10/systemd 257: systemctl --machine=<user>@ is interpreted as a local
	// container name, not a user manager. Use runuser with an explicit
	// XDG_RUNTIME_DIR to target the rootless user's systemd --user session.
	lookup := m.lookupUser
	if lookup == nil {
		lookup = user.Lookup
	}
	u, err := lookup(m.RunAsUser)
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w", m.RunAsUser, err)
	}
	xdgRuntimeDir := "/run/user/" + u.Uid
	runuserArgs := append([]string{"-u", m.RunAsUser, "--", "env", "XDG_RUNTIME_DIR=" + xdgRuntimeDir, "systemctl", "--user"}, args...)
	return runner.Run(ctx, "runuser", runuserArgs...)
}

func (m *Manager) ensureLingerEnabled(ctx context.Context, runner Runner) error {
	if strings.TrimSpace(m.RunAsUser) == "" {
		return nil
	}
	timeout := m.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	lingerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := runner.Run(lingerCtx, "loginctl", "enable-linger", m.RunAsUser)
	if err != nil {
		return fmt.Errorf("enable lingering for %q: %w", m.RunAsUser, err)
	}
	return nil
}

func (m *Manager) rootlessDaemonReload(ctx context.Context) error {
	runner := m.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	if err := m.ensureLingerEnabled(ctx, runner); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		_, err := m.runAsUserCommand(ctx, runner, "daemon-reload")
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientRootlessReloadError(err) || attempt == 1 {
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

func isMissingRootlessUnitError(err error) bool {
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "NOTINSTALLED") ||
		strings.Contains(msg, "NOT FOUND") ||
		strings.Contains(msg, "UNIT ") && strings.Contains(msg, " NOT FOUND") ||
		strings.Contains(msg, "UNIT ") && strings.Contains(msg, " NOT AVAILABLE")
}
