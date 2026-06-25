// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
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

type Inspector struct {
	Runner       Runner
	Timeout      time.Duration
	RootlessUser string // empty = run as root; set = run via sudo -u
}

func NewInspector(runner Runner) *Inspector {
	return &Inspector{Runner: runner, Timeout: 30 * time.Second}
}

// Login authenticates to a private container registry using podman login.
// Credentials are stored in the rootless user's auth.json for subsequent
// image pulls by Quadlet or podman.
func (i *Inspector) Login(ctx context.Context, server, username, password string) error {
	_, err := i.runWithStdin(ctx, strings.NewReader(password), "login", "--username", username, "--password-stdin", server)
	if err != nil {
		return fmt.Errorf("podman login to %q: %w", server, err)
	}
	return nil
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
	return i.ExecWithEnv(ctx, container, nil, args...)
}

func (i *Inspector) ExecWithEnv(ctx context.Context, container string, env map[string]string, args ...string) ([]byte, error) {
	return i.execWithInput(ctx, container, env, nil, args...)
}

func (i *Inspector) ExecWithStdin(ctx context.Context, container string, env map[string]string, stdin io.Reader, args ...string) ([]byte, error) {
	return i.execWithInput(ctx, container, env, stdin, args...)
}

// ExecTrustedShell executes a trusted shell command inside a container.
// It is intended only for setup_command payloads coming from validated app
// definitions stored in admirald, where shell features like variable expansion
// and boolean chaining are explicitly part of the contract.
func (i *Inspector) ExecTrustedShell(ctx context.Context, container, command string) ([]byte, error) {
	return i.execTrustedWithInput(ctx, container, nil, nil, "sh", "-c", command)
}

// RunTrustedInPod runs a one-off helper container inside an existing pod with
// trusted arguments, inherited service mounts, and explicit environment.
func (i *Inspector) RunTrustedInPod(ctx context.Context, pod, image string, env map[string]string, mounts []string, args ...string) ([]byte, error) {
	cmdArgs := []string{"run", "--rm", "--pod", pod}

	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", key, env[key]))
		}
	}

	for _, mount := range mounts {
		if strings.TrimSpace(mount) == "" {
			continue
		}
		cmdArgs = append(cmdArgs, "-v", mount)
	}

	cmdArgs = append(cmdArgs, image)
	cmdArgs = append(cmdArgs, args...)
	return i.runTrustedWithStdin(ctx, nil, cmdArgs...)
}

// RunTrustedInPodNoEntrypoint is like RunTrustedInPod but passes
// --entrypoint "" to skip the image's entrypoint. This avoids triggering
// heavy initialization (e.g. MariaDB db init) in one-shot helper containers
// used for healthchecks.
func (i *Inspector) RunTrustedInPodNoEntrypoint(ctx context.Context, pod, image string, env map[string]string, mounts []string, args ...string) ([]byte, error) {
	cmdArgs := []string{"run", "--rm", "--pod", pod}

	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", key, env[key]))
		}
	}

	for _, mount := range mounts {
		if strings.TrimSpace(mount) == "" {
			continue
		}
		cmdArgs = append(cmdArgs, "-v", mount)
	}

	cmdArgs = append(cmdArgs, "--entrypoint", "")
	cmdArgs = append(cmdArgs, image)
	cmdArgs = append(cmdArgs, args...)
	return i.runTrustedWithStdin(ctx, nil, cmdArgs...)
}

func (i *Inspector) RunTrustedShellInPod(ctx context.Context, pod, image string, env map[string]string, mounts []string, command string) ([]byte, error) {
	return i.RunTrustedInPod(ctx, pod, image, env, mounts, "sh", "-c", command)
}

func (i *Inspector) execWithInput(ctx context.Context, container string, env map[string]string, stdin io.Reader, args ...string) ([]byte, error) {
	cmdArgs := []string{"exec"}
	if stdin != nil {
		cmdArgs = append(cmdArgs, "-i")
	}

	var envFile string
	if len(env) > 0 {
		f, err := os.CreateTemp("", "admiral-env-")
		if err != nil {
			return nil, fmt.Errorf("create temp env file: %w", err)
		}
		envFile = f.Name()
		defer os.Remove(envFile)

		if err := f.Chmod(0600); err != nil {
			return nil, fmt.Errorf("chmod temp env file: %w", err)
		}

		for k, v := range env {
			if _, err := f.WriteString(fmt.Sprintf("%s=%s\n", k, v)); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("write temp env file: %w", err)
			}
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close temp env file: %w", err)
		}
		cmdArgs = append(cmdArgs, "--env-file", envFile)
	}

	cmdArgs = append(cmdArgs, container)
	cmdArgs = append(cmdArgs, args...)
	return i.runWithStdin(ctx, stdin, cmdArgs...)
}

func (i *Inspector) execTrustedWithInput(ctx context.Context, container string, env map[string]string, stdin io.Reader, args ...string) ([]byte, error) {
	cmdArgs := []string{"exec"}
	if stdin != nil {
		cmdArgs = append(cmdArgs, "-i")
	}

	var envFile string
	if len(env) > 0 {
		f, err := os.CreateTemp("", "admiral-env-")
		if err != nil {
			return nil, fmt.Errorf("create temp env file: %w", err)
		}
		envFile = f.Name()
		defer os.Remove(envFile)

		if err := f.Chmod(0600); err != nil {
			return nil, fmt.Errorf("chmod temp env file: %w", err)
		}

		for k, v := range env {
			if _, err := f.WriteString(fmt.Sprintf("%s=%s\n", k, v)); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("write temp env file: %w", err)
			}
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close temp env file: %w", err)
		}
		cmdArgs = append(cmdArgs, "--env-file", envFile)
	}

	cmdArgs = append(cmdArgs, container)
	cmdArgs = append(cmdArgs, args...)
	return i.runTrustedWithStdin(ctx, stdin, cmdArgs...)
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

// SecretCreate creates a Podman secret with the given name and value.
// The secret is stored encrypted in the Podman secret store for the
// rootless user (or root if no RootlessUser is set).
// Using --replace makes this idempotent: if the secret already exists,
// it is replaced silently.
func (i *Inspector) SecretCreate(ctx context.Context, name, value string) error {
	_, err := i.runWithStdin(ctx, strings.NewReader(value), "secret", "create", "--replace", name, "-")
	if err != nil {
		return fmt.Errorf("create podman secret %q: %w", name, err)
	}
	return nil
}

// SecretRemove removes a Podman secret by name.
// Errors are returned as-is (caller should ignore not-found if idempotency is desired).
func (i *Inspector) SecretRemove(ctx context.Context, name string) error {
	_, err := i.run(ctx, "secret", "rm", name)
	return err
}

func (i *Inspector) PodPause(ctx context.Context, podName string) error {
	_, err := i.run(ctx, "pod", "pause", podName)
	return err
}

func (i *Inspector) PodUnpause(ctx context.Context, podName string) error {
	_, err := i.run(ctx, "pod", "unpause", podName)
	return err
}

func (i *Inspector) PodIsPaused(ctx context.Context, podName string) (bool, error) {
	out, err := i.run(ctx, "pod", "inspect", podName, "--format", "{{.State}}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "Paused", nil
}

func (i *Inspector) run(ctx context.Context, args ...string) ([]byte, error) {
	return i.runWithStdin(ctx, nil, args...)
}

func (i *Inspector) runWithStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	timeout := i.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if i.RootlessUser != "" {
		return i.runAsUserWithStdin(runCtx, stdin, args...)
	}

	runner := i.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	cr, ok := runner.(*CommandRunner)
	if ok {
		return cr.runWithStdin(runCtx, stdin, "podman", args...)
	}
	if stdin != nil {
		if runnerWithStdin, ok := runner.(stdinRunner); ok {
			return runnerWithStdin.RunWithStdin(runCtx, stdin, "podman", args...)
		}
		return nil, fmt.Errorf("runner %T does not support stdin", runner)
	}
	return runner.Run(runCtx, "podman", args...)
}

func (i *Inspector) runTrustedWithStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	timeout := i.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if i.RootlessUser != "" {
		return i.runAsUserWithStdinTrusted(runCtx, stdin, args...)
	}

	runner := i.Runner
	if runner != nil {
		if stdin != nil {
			if runnerWithStdin, ok := runner.(stdinRunner); ok {
				return runnerWithStdin.RunWithStdin(runCtx, stdin, "podman", args...)
			}
			return nil, fmt.Errorf("runner %T does not support stdin", runner)
		}
		return runner.Run(runCtx, "podman", args...)
	}

	return trustedCommand(runCtx, stdin, "podman", args...)
}

func (i *Inspector) runAsUserWithStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	// Use systemd-run to execute podman inside the rootless user's systemd
	// session. This ensures podman uses cgroup-manager=systemd, matching the
	// cgroup manager used by Quadlet. Without a user systemd session, podman
	// falls back to cgroup-manager=cgroupfs, which causes:
	//   "systemd slice received as cgroup parent when using cgroupfs"
	// when running one-shot containers with --pod (healthchecks) or
	// podman exec on Quadlet-started containers.
	sdrunArgs := append([]string{
		"--machine", i.RootlessUser + "@",
		"--user",
		"--wait",
		"--collect",
		"--pipe",
		"--",
		"podman",
	}, args...)

	runner := i.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	cr, ok := runner.(*CommandRunner)
	if ok {
		return cr.runWithStdin(ctx, stdin, "systemd-run", sdrunArgs...)
	}
	if stdin != nil {
		if runnerWithStdin, ok := runner.(stdinRunner); ok {
			return runnerWithStdin.RunWithStdin(ctx, stdin, "systemd-run", sdrunArgs...)
		}
		return nil, fmt.Errorf("runner %T does not support stdin", runner)
	}
	return runner.Run(ctx, "systemd-run", sdrunArgs...)
}

func (i *Inspector) runAsUserWithStdinTrusted(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	sdrunArgs := append([]string{
		"--machine", i.RootlessUser + "@",
		"--user",
		"--wait",
		"--collect",
		"--pipe",
		"--",
		"podman",
	}, args...)

	runner := i.Runner
	if runner != nil {
		if stdin != nil {
			if runnerWithStdin, ok := runner.(stdinRunner); ok {
				return runnerWithStdin.RunWithStdin(ctx, stdin, "systemd-run", sdrunArgs...)
			}
			return nil, fmt.Errorf("runner %T does not support stdin", runner)
		}
		return runner.Run(ctx, "systemd-run", sdrunArgs...)
	}

	return trustedCommand(ctx, stdin, "systemd-run", sdrunArgs...)
}

func trustedCommand(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	sanitizedArgs := security.SanitizeArgs(args)
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- trusted internal execution path for validated setup_command
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
