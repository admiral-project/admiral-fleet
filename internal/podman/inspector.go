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
	"sort"
	"strconv"
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

type trustedStdinRunner interface {
	RunTrustedWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
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
	Runner         Runner
	Timeout        time.Duration
	RootlessUser   string // empty = run as root; set = run via sudo -u
	TempDir        string // shared with the rootless user manager when PrivateTmp is enabled
	RemoteRootless bool   // delegate every Podman invocation to a rootless helper
}

// IDMapEntry describes one user-namespace mapping line. Container IDs map to
// host IDs; restore uses the inverse mapping for TAR headers created outside
// the namespace.
type IDMapEntry struct {
	ContainerStart uint64
	HostStart      uint64
	Count          uint64
}

func (entry IDMapEntry) HostToContainer(id uint64) (uint64, bool) {
	if id < entry.HostStart || id-entry.HostStart >= entry.Count {
		return 0, false
	}
	return entry.ContainerStart + (id - entry.HostStart), true
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

// Pull downloads an image through the configured Podman runner. When the
// runner is RemoteRootless, the lifecycle helper executes this operation as
// the configured rootless workload user.
func (i *Inspector) Pull(ctx context.Context, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("image reference is required")
	}
	if _, err := i.run(ctx, "pull", image); err != nil {
		return fmt.Errorf("pull image %q: %w", image, err)
	}
	return nil
}

func (i *Inspector) ContainerExists(ctx context.Context, container string) error {
	_, err := i.run(ctx, "container", "exists", container)
	return err
}

func (i *Inspector) VolumeInspect(ctx context.Context, volume string) ([]byte, error) {
	return i.run(ctx, "volume", "inspect", volume, "--format", "json")
}

// ExtractTar restores an archive inside Podman's rootless user namespace.
// Volume files may be owned by subordinate IDs that are not writable by the
// host-side rootless user. Running tar through podman unshare lets the kernel
// apply those mapped IDs while keeping the operation rootless.
func (i *Inspector) ExtractTar(ctx context.Context, archive io.Reader, mountpoint string) error {
	if strings.TrimSpace(mountpoint) == "" || !filepath.IsAbs(mountpoint) {
		return fmt.Errorf("invalid volume mountpoint %q", mountpoint)
	}
	_, err := i.runTrustedWithStdin(ctx, archive, "unshare", "tar", "--extract", "--file=-", "--directory", mountpoint, "--same-owner", "--no-same-permissions")
	if err != nil {
		return fmt.Errorf("extract archive in rootless user namespace: %w", err)
	}
	return nil
}

// UserNamespaceIDMap returns the effective rootless user namespace mappings.
func (i *Inspector) UserNamespaceIDMap(ctx context.Context) ([]IDMapEntry, error) {
	return i.userNamespaceMap(ctx, "uid_map")
}

// UserNamespaceGIDMap returns the effective rootless group namespace mappings.
func (i *Inspector) UserNamespaceGIDMap(ctx context.Context) ([]IDMapEntry, error) {
	return i.userNamespaceMap(ctx, "gid_map")
}

func (i *Inspector) userNamespaceMap(ctx context.Context, name string) ([]IDMapEntry, error) {
	out, err := i.runTrustedWithStdin(ctx, nil, "unshare", "cat", "/proc/self/"+name)
	if err != nil {
		return nil, fmt.Errorf("read rootless %s: %w", name, err)
	}
	var entries []IDMapEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		containerStart, err1 := strconv.ParseUint(fields[0], 10, 64)
		hostStart, err2 := strconv.ParseUint(fields[1], 10, 64)
		count, err3 := strconv.ParseUint(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || count == 0 {
			return nil, fmt.Errorf("invalid rootless user namespace map line %q", line)
		}
		entries = append(entries, IDMapEntry{ContainerStart: containerStart, HostStart: hostStart, Count: count})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("rootless %s is empty", name)
	}
	return entries, nil
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
// If containerUser is non-empty, it is passed as --user to podman run.
func (i *Inspector) RunTrustedInPod(ctx context.Context, pod, image string, env map[string]string, mounts []string, containerUser string, args ...string) ([]byte, error) {
	cmdArgs := []string{"run", "--rm", "--pod", pod}
	var envFile string
	if len(env) > 0 {
		var err error
		envFile, err = i.writeTempEnvFile(env)
		if err != nil {
			return nil, err
		}
		defer os.Remove(envFile)
		cmdArgs = append(cmdArgs, "--env-file", envFile)
	}

	if containerUser != "" {
		cmdArgs = append(cmdArgs, "--user", containerUser)
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
func (i *Inspector) RunTrustedInPodNoEntrypoint(ctx context.Context, pod, image string, env map[string]string, mounts []string, containerUser string, args ...string) ([]byte, error) {
	cmdArgs := []string{"run", "--rm", "--pod", pod}
	var envFile string
	if len(env) > 0 {
		var err error
		envFile, err = i.writeTempEnvFile(env)
		if err != nil {
			return nil, err
		}
		defer os.Remove(envFile)
		cmdArgs = append(cmdArgs, "--env-file", envFile)
	}

	if containerUser != "" {
		cmdArgs = append(cmdArgs, "--user", containerUser)
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

func (i *Inspector) RunTrustedShellInPod(ctx context.Context, pod, image string, env map[string]string, mounts []string, containerUser string, command string) ([]byte, error) {
	return i.RunTrustedInPod(ctx, pod, image, env, mounts, containerUser, "sh", "-c", command)
}

func (i *Inspector) execWithInput(ctx context.Context, container string, env map[string]string, stdin io.Reader, args ...string) ([]byte, error) {
	cmdArgs := []string{"exec"}
	if stdin != nil {
		cmdArgs = append(cmdArgs, "-i")
	}

	var envFile string
	if len(env) > 0 {
		f, err := i.createEnvFile()
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
		f, err := i.createEnvFile()
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

func (i *Inspector) createEnvFile() (*os.File, error) {
	tempDir := i.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if err := os.MkdirAll(tempDir, 0711); err != nil {
		return nil, fmt.Errorf("create shared temp dir: %w", err)
	}
	f, err := os.CreateTemp(tempDir, "admiral-env-")
	if err != nil {
		return nil, err
	}
	if i.RootlessUser == "" {
		return f, nil
	}
	u, err := user.Lookup(i.RootlessUser)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("lookup rootless user %q: %w", i.RootlessUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("parse uid for rootless user %q: %w", i.RootlessUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("parse gid for rootless user %q: %w", i.RootlessUser, err)
	}
	if err := f.Chown(uid, gid); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("chown temp env file to rootless user %q: %w", i.RootlessUser, err)
	}
	return f, nil
}

func (i *Inspector) writeTempEnvFile(env map[string]string) (string, error) {
	f, err := i.createEnvFile()
	if err != nil {
		return "", fmt.Errorf("create temp env file: %w", err)
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("chmod temp env file: %w", err)
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(f, "%s=%s\n", key, env[key]); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return "", fmt.Errorf("write temp env file: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close temp env file: %w", err)
	}
	return f.Name(), nil
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
	_, err := i.runSecretWithStdin(ctx, strings.NewReader(value), "create", "--replace", name, "-")
	if err != nil {
		return fmt.Errorf("create podman secret %q: %w", name, err)
	}
	return nil
}

// SecretRemove removes a Podman secret by name.
// Errors are returned as-is (caller should ignore not-found if idempotency is desired).
func (i *Inspector) SecretRemove(ctx context.Context, name string) error {
	_, err := i.runSecretWithStdin(ctx, nil, "rm", name)
	return err
}

func (i *Inspector) runSecretWithStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	timeout := i.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	secretArgs := append([]string{"secret"}, args...)
	if i.RootlessUser != "" && !i.RemoteRootless {
		return i.runAsUserSystemdUserSession(runCtx, stdin, secretArgs...)
	}

	runner := i.Runner
	if runner == nil {
		runner = CommandRunner{}
	}
	cr, ok := runner.(*CommandRunner)
	if ok {
		return cr.runWithStdin(runCtx, stdin, "podman", secretArgs...)
	}
	if stdin != nil {
		if runnerWithStdin, ok := runner.(stdinRunner); ok {
			return runnerWithStdin.RunWithStdin(runCtx, stdin, "podman", secretArgs...)
		}
		return nil, fmt.Errorf("runner %T does not support stdin", runner)
	}
	return runner.Run(runCtx, "podman", secretArgs...)
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

	if i.RootlessUser != "" && !i.RemoteRootless {
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

	if i.RootlessUser != "" && !i.RemoteRootless {
		return i.runAsUserWithStdinTrusted(runCtx, stdin, args...)
	}

	runner := i.Runner
	if runner != nil {
		if trustedRunner, ok := runner.(trustedStdinRunner); ok {
			return trustedRunner.RunTrustedWithStdin(runCtx, stdin, "podman", args...)
		}
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
	u, err := user.Lookup(i.RootlessUser)
	if err != nil {
		return nil, fmt.Errorf("lookup rootless user %q: %w", i.RootlessUser, err)
	}
	xdgRuntimeDir := filepath.Join("/run/user", u.Uid)

	// Detect if this is an exec operation that needs the user's systemd
	// session for cgroup access. Quadlet containers use systemd cgroup
	// manager; running podman exec via runuser falls back to cgroupfs
	// and fails with "systemd slice received as cgroup parent when
	// using cgroupfs".
	if len(args) > 0 && args[0] == "exec" {
		return i.runAsUserSystemdUserSession(ctx, stdin, args...)
	}

	// Use runuser to run podman as the rootless user, with XDG_RUNTIME_DIR set
	// so podman can find the user's runtime directory (rootless containers).
	// This path is safe for commands that don't interact with Quadlet cgroups
	// (e.g. podman pod exists, podman container exists).

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

// runAsUserSystemdUserSession starts a transient user-manager unit in the
// rootless user's own systemd session. This keeps podman secret operations
// and exec operations attached to the rootless runtime and avoids the
// unreliable systemd-machined transport from the fleet service sandbox.
func (i *Inspector) runAsUserSystemdUserSession(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	u, err := user.Lookup(i.RootlessUser)
	if err != nil {
		return nil, fmt.Errorf("lookup rootless user %q: %w", i.RootlessUser, err)
	}
	xdgRuntimeDir := filepath.Join("/run/user", u.Uid)
	dbusSessionBus := filepath.Join(xdgRuntimeDir, "bus")

	systemdArgs := append([]string{
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
	runuserArgs := append([]string{
		"-u", i.RootlessUser, "--",
		"env",
		"XDG_RUNTIME_DIR=" + xdgRuntimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + dbusSessionBus,
		"systemd-run",
	}, systemdArgs...)

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

func (i *Inspector) runAsUserWithStdinTrusted(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	// Detect if this is an exec operation that needs the user's systemd
	// session for cgroup access. Quadlet containers use systemd cgroup
	// manager; running podman exec via runuser falls back to cgroupfs
	// and fails with "systemd slice received as cgroup parent when
	// using cgroupfs".
	if len(args) > 0 && args[0] == "exec" {
		return i.runAsUserSystemdUserSession(ctx, stdin, args...)
	}

	// Use runuser to run podman as the rootless user, with XDG_RUNTIME_DIR set
	// so podman can find the user's runtime directory (rootless containers).
	// This path is safe for commands that don't interact with Quadlet cgroups
	// (e.g. podman run --rm --pod for healthchecks).
	u, err := user.Lookup(i.RootlessUser)
	if err != nil {
		return nil, fmt.Errorf("lookup rootless user %q: %w", i.RootlessUser, err)
	}
	xdgRuntimeDir := filepath.Join("/run/user", u.Uid)
	runuserArgs := append([]string{"-u", i.RootlessUser, "--", "env", "XDG_RUNTIME_DIR=" + xdgRuntimeDir, "podman"}, args...)

	runner := i.Runner
	if runner != nil {
		if stdin != nil {
			if runnerWithStdin, ok := runner.(stdinRunner); ok {
				return runnerWithStdin.RunWithStdin(ctx, stdin, "runuser", runuserArgs...)
			}
			return nil, fmt.Errorf("runner %T does not support stdin", runner)
		}
		return runner.Run(ctx, "runuser", runuserArgs...)
	}

	return trustedCommand(ctx, stdin, "runuser", runuserArgs...)
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
