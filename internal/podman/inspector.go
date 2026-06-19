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
	"os/user"
	"path/filepath"
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

func (i *Inspector) execWithInput(ctx context.Context, container string, env map[string]string, stdin io.Reader, args ...string) ([]byte, error) {
	cmdArgs := []string{"exec"}
	if stdin != nil {
		cmdArgs = append(cmdArgs, "-i")
	}

	if len(env) > 0 {
		var envContent strings.Builder
		for k, v := range env {
			envContent.WriteString(fmt.Sprintf("%s=%s\n", k, v))
		}
		// Create a temporary environment file to avoid leaking secrets in ps
		tmpEnv, err := os.CreateTemp("", "admiral-env-")
		if err != nil {
			return nil, fmt.Errorf("create temp env file: %w", err)
		}
		defer os.Remove(tmpEnv.Name())
		if _, err := tmpEnv.WriteString(envContent.String()); err != nil {
			_ = tmpEnv.Close()
			return nil, fmt.Errorf("write temp env file: %w", err)
		}
		_ = tmpEnv.Close()
		cmdArgs = append(cmdArgs, "--env-file", tmpEnv.Name())
	}

	cmdArgs = append(cmdArgs, container)
	cmdArgs = append(cmdArgs, args...)
	return i.runWithStdin(ctx, stdin, cmdArgs...)
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

func (i *Inspector) runAsUserWithStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	u, err := user.Lookup(i.RootlessUser)
	if err != nil {
		return nil, fmt.Errorf("lookup rootless user %q: %w", i.RootlessUser, err)
	}
	xdgRuntimeDir := filepath.Join("/run/user", u.Uid)
	// Use runuser to run podman as the rootless user, with XDG_RUNTIME_DIR set
	// so podman can find the user's runtime directory (rootless containers).

	// We MUST NOT sanitize here, because this is the WRAPPER.
	// The ACTUAL runner (CommandRunner) will sanitize the final arguments.
	runuserArgs := append([]string{"-u", i.RootlessUser, "--", "env", "XDG_RUNTIME_DIR=" + xdgRuntimeDir, "podman"}, args...)

	runner := i.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	cr, ok := runner.(*CommandRunner)
	if ok {
		return cr.runWithStdin(ctx, stdin, "runuser", runuserArgs...)
	}
	if stdin != nil {
		if runnerWithStdin, ok := runner.(stdinRunner); ok {
			return runnerWithStdin.RunWithStdin(ctx, stdin, "runuser", runuserArgs...)
		}
		return nil, fmt.Errorf("runner %T does not support stdin", runner)
	}
	return runner.Run(ctx, "runuser", runuserArgs...)
}
