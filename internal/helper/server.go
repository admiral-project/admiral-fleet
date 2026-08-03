// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/rootlessprotocol"
	"github.com/admiral-project/admiral/admiral-fleet/internal/security"
)

const maxRequestBytes = 1 << 30

// Serve handles one rootless Podman request. The executable using it must be
// launched as the workload user and must expose no network listener.
func Serve(ctx context.Context, allowed, trusted map[string]bool) error {
	stream := bufio.NewReader(io.LimitReader(os.Stdin, maxRequestBytes+1))
	header, err := stream.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read helper request header: %w", err)
	}
	var request rootlessprotocol.Request
	if err := json.Unmarshal(header, &request); err != nil {
		return fmt.Errorf("parse helper request: %w", err)
	}
	if request.Version != rootlessprotocol.Version {
		return fmt.Errorf("unsupported helper request version %d", request.Version)
	}
	if request.Name != "podman" {
		return fmt.Errorf("unsupported helper executable %q", request.Name)
	}
	if len(request.Args) == 0 || !allowed[strings.TrimSpace(request.Args[0])] {
		return fmt.Errorf("operation %q is not allowed for this helper", firstArg(request.Args))
	}
	if !trusted[strings.TrimSpace(request.Args[0])] {
		if err := security.ValidateExecParams(request.Name, request.Args); err != nil {
			return err
		}
	}
	var out []byte
	var runErr error
	runner := podman.UserSessionRunner{}
	out, runErr = runner.RunWithStdin(ctx, stream, request.Name, request.Args...)
	response := rootlessprotocol.Response{Version: rootlessprotocol.Version, Stdout: out}
	if runErr != nil {
		response.Error = runErr.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		return fmt.Errorf("write helper response: %w", err)
	}
	if runErr != nil {
		return runErr
	}
	return nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
