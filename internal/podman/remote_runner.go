// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/rootlessprotocol"
)

const (
	DefaultLifecycleHelper = "/usr/bin/admiral-fleet-lifecycle"
	DefaultSetupHelper     = "/usr/bin/admiral-fleet-setup"
)

// RemoteRunner delegates every Podman invocation to a specialized helper that
// runs as the rootless workload user. The caller remains responsible for
// building the validated Podman argument list; the helper only accepts the
// operation domain assigned to it.
type RemoteRunner struct {
	RootlessUser string
	DataDir      string
	Lifecycle    string
	Setup        string
}

func (r RemoteRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, nil, false, name, args...)
}

func (r RemoteRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, stdin, false, name, args...)
}

// RunTrustedWithStdin is used only for validated setup commands whose shell
// syntax is part of the app definition contract.
func (r RemoteRunner) RunTrustedWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, stdin, true, name, args...)
}

func (r RemoteRunner) runWithStdin(ctx context.Context, stdin io.Reader, trusted bool, name string, args ...string) ([]byte, error) {
	if name != "podman" {
		return nil, fmt.Errorf("rootless helper accepts only podman, got %q", name)
	}
	helper, err := r.helperFor(args)
	if err != nil {
		return nil, err
	}
	var input []byte
	if stdin != nil {
		input, err = io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read Podman stdin: %w", err)
		}
	}
	payload, err := json.Marshal(rootlessprotocol.Request{
		Version: rootlessprotocol.Version,
		Name:    name,
		Args:    args,
		Stdin:   input,
		Trusted: trusted,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal rootless Podman request: %w", err)
	}
	uid, err := lookupUserID(r.RootlessUser)
	if err != nil {
		return nil, err
	}
	dataDir := r.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/var/lib/admiral"
	}
	argsForRunuser := []string{
		"-u", r.RootlessUser, "--", "env",
		"HOME=/var/lib/admiral-apps",
		"XDG_RUNTIME_DIR=" + filepath.Join("/run/user", uid),
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join("/run/user", uid, "bus"),
		"ADMIRAL_FLEET_DATA_DIR=" + dataDir,
		helper,
	}
	out, runErr := (CommandRunner{}).RunWithStdin(ctx, bytes.NewReader(payload), "runuser", argsForRunuser...)
	if runErr != nil {
		var response rootlessprotocol.Response
		if json.Unmarshal(out, &response) == nil && response.Error != "" {
			return response.Stdout, fmt.Errorf("rootless Podman helper %q: %s", filepath.Base(helper), response.Error)
		}
		return nil, fmt.Errorf("run rootless Podman helper %q: %w", filepath.Base(helper), runErr)
	}
	var response rootlessprotocol.Response
	if err := json.Unmarshal(out, &response); err != nil {
		return nil, fmt.Errorf("parse rootless Podman response: %w", err)
	}
	if response.Version != rootlessprotocol.Version {
		return nil, fmt.Errorf("unsupported rootless Podman response version %d", response.Version)
	}
	if response.Error != "" {
		return response.Stdout, fmt.Errorf("rootless Podman helper: %s", response.Error)
	}
	return response.Stdout, nil
}

func (r RemoteRunner) helperFor(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("Podman operation is required")
	}
	var helper string
	switch args[0] {
	case "exec", "cp", "run", "login", "secret":
		helper = r.Setup
		if strings.TrimSpace(helper) == "" {
			helper = DefaultSetupHelper
		}
	case "version", "port", "pod", "ps", "container", "volume", "rm":
		helper = r.Lifecycle
		if strings.TrimSpace(helper) == "" {
			helper = DefaultLifecycleHelper
		}
	default:
		return "", fmt.Errorf("Podman operation %q has no rootless helper", args[0])
	}
	return helper, nil
}

func lookupUserID(username string) (string, error) {
	if strings.TrimSpace(username) == "" {
		return "", fmt.Errorf("rootless user is required")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return "", fmt.Errorf("lookup rootless user %q: %w", username, err)
	}
	return u.Uid, nil
}
