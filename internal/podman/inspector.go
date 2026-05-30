package podman

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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
	return &Inspector{Runner: runner, Timeout: 30 * time.Second}
}

func (i *Inspector) PodPort(ctx context.Context, podName, containerPort string) (string, error) {
	out, err := i.run(ctx, "port", podName, containerPort)
	if err != nil {
		return "", fmt.Errorf("get pod port %q for pod %q: %w", containerPort, podName, err)
	}
	return strings.TrimSpace(string(out)), nil
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

func (i *Inspector) ContainerInspect(ctx context.Context, container string) ([]byte, error) {
	return i.run(ctx, "container", "inspect", container, "--format", "json")
}

func (i *Inspector) ContainerExists(ctx context.Context, container string) error {
	_, err := i.run(ctx, "container", "exists", container)
	return err
}

func (i *Inspector) VolumeInspect(ctx context.Context, volume string) ([]byte, error) {
	return i.run(ctx, "volume", "inspect", volume, "--format", "json")
}

func (i *Inspector) Exec(ctx context.Context, container string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"exec", container}, args...)
	return i.run(ctx, cmdArgs...)
}

func (i *Inspector) CopyToContainer(ctx context.Context, sourcePath, containerPath string) ([]byte, error) {
	return i.run(ctx, "cp", sourcePath, containerPath)
}

func (i *Inspector) RemovePod(ctx context.Context, podName string) error {
	_, err := i.run(ctx, "pod", "rm", "--force", podName)
	return err
}

func (i *Inspector) RemoveContainer(ctx context.Context, name string) error {
	_, err := i.run(ctx, "rm", "--force", name)
	return err
}

func (i *Inspector) RemoveVolume(ctx context.Context, name string) error {
	_, err := i.run(ctx, "volume", "rm", "--force", name)
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
