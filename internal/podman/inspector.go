package podman

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

type Inspector struct {
	Runner  Runner
	Timeout time.Duration
}

func NewInspector(runner Runner) *Inspector {
	return &Inspector{
		Runner:  runner,
		Timeout: 30 * time.Second,
	}
}

func (i *Inspector) PodExists(ctx context.Context, podName string) error {
	_, err := i.run(ctx, "pod", "exists", podName)
	return err
}

func (i *Inspector) PodPS(ctx context.Context) ([]byte, error) {
	return i.run(ctx, "pod", "ps", "--format", "json")
}

func (i *Inspector) ContainerPS(ctx context.Context) ([]byte, error) {
	return i.run(ctx, "ps", "--format", "json")
}

func (i *Inspector) RemovePod(ctx context.Context, podName string) error {
	_, err := i.run(ctx, "pod", "rm", "--force", podName)
	return err
}

func (i *Inspector) run(ctx context.Context, args ...string) ([]byte, error) {
	timeout := i.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runner := i.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	return runner.Run(runCtx, "podman", args...)
}
