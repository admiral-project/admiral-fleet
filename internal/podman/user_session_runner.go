// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
)

// UserSessionRunner runs commands inside the caller's systemd user manager.
// It is used by admiral-fleet-backup, which already runs as the rootless
// workload user, so podman operations stay attached to the rootless runtime
// and avoid the cgroupfs fallback that breaks podman exec on Quadlet units.
type UserSessionRunner struct{}

func (r UserSessionRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, nil, name, args...)
}

func (r UserSessionRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, stdin, name, args...)
}

func (r UserSessionRunner) runWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	if err := security.ValidateExecParams(name, args); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "systemctl", userSessionCommandArgs(name, args)...) // #nosec G204 -- name and args are validated by security.ValidateExecParams
	cmd.Dir = "/tmp"
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		sanitizedArgs := security.SanitizeArgs(args)
		if stderr.Len() > 0 {
			sanitizedStderr := security.Sanitize(stderr.String())
			return out, fmt.Errorf("%s %v: %w: %s", name, sanitizedArgs, err, sanitizedStderr)
		}
		return out, fmt.Errorf("%s %v: %w", name, sanitizedArgs, err)
	}
	return out, nil
}

// userSessionCommandArgs runs the command as a transient unit in the caller's
// systemd user manager, piping stdio so exec/cp operations keep working.
func userSessionCommandArgs(name string, args []string) []string {
	return append([]string{"--user", "--wait", "--collect", "--pipe", "--", name}, args...)
}
