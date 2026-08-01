// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
)

// UserRunner runs systemctl against the caller's own systemd user manager.
// It is used by admiral-fleet-backup, which already runs as the rootless
// workload user and talks to the user bus directly.
type UserRunner struct{}

func (r UserRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, nil, name, args...)
}

func (r UserRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.runWithStdin(ctx, stdin, name, args...)
}

func (r UserRunner) runWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	if err := security.ValidateExecParams(name, args); err != nil {
		return nil, err
	}
	// name is expected to be "systemctl"; run against the user manager.
	cmd := exec.CommandContext(ctx, name, userCommandArgs(args)...) // #nosec G204 -- name and args are validated by security.ValidateExecParams
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

// userCommandArgs prepends --user so systemctl talks to the caller's own
// systemd user manager.
func userCommandArgs(args []string) []string {
	return append([]string{"--user"}, args...)
}
