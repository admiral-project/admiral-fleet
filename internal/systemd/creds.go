// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func EncryptCred(ctx context.Context, runner Runner, name string, stdin io.Reader, outputPath string) error {
	if runner == nil {
		runner = CommandRunner{}
	}

	args := []string{"encrypt", "--name=" + name, "-", outputPath}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if cr, ok := runner.(*CommandRunner); ok {
		_, err := cr.runWithStdin(runCtx, stdin, "systemd-creds", args...)
		return err
	}

	if runnerWithStdin, ok := runner.(stdinRunner); ok {
		_, err := runnerWithStdin.RunWithStdin(runCtx, stdin, "systemd-creds", args...)
		return err
	}

	return fmt.Errorf("runner %T does not support stdin", runner)
}

func RemoveCred(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cred %q: %w", path, err)
	}
	return nil
}

func CredDir(dataDir, instanceID string) string {
	return fmt.Sprintf("%s/instances/%s/creds", strings.TrimRight(dataDir, "/"), instanceID)
}

func CredFilePath(dataDir, instanceID, serviceName, envName string) string {
	return fmt.Sprintf("%s/%s-%s.cred", CredDir(dataDir, instanceID), serviceName, envName)
}
