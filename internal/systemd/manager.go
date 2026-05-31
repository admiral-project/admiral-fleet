package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type CommandRunner struct{}

func (r CommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return out, fmt.Errorf("%s %v: %w: %s", name, args, err, stderr.String())
		}
		return out, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return out, nil
}

type Manager struct {
	Runner     Runner
	Timeout    time.Duration
	RunAsUser  string // empty = rootful systemd; set = rootless user systemd --user
}

func NewManager(runner Runner) *Manager {
	return &Manager{
		Runner:  runner,
		Timeout: 30 * time.Second,
	}
}

func (m *Manager) DaemonReload(ctx context.Context) error {
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

	name := "systemctl"
	cmdArgs := args

	if m.RunAsUser != "" {
		name = "sudo"
		cmdArgs = append([]string{"-u", m.RunAsUser, "systemctl", "--user"}, args...)
	}

	runner := m.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	return runner.Run(runCtx, name, cmdArgs...)
}
