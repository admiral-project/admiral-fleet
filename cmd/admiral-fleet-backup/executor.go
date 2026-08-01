// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admiral-fleet/internal/osutil"
	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
)

// buildExecutor returns an executor that talks to Podman and systemd directly
// in the caller's user session. The fleet passes DataDir and the rootless user
// via environment when spawning the helper.
func buildExecutor() *executor.SystemdPodmanExecutor {
	rootlessUser := envOr("ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	dataDir := envOr("ADMIRAL_FLEET_DATA_DIR", "/var/lib/admiral")

	insp := podman.NewInspector(podman.UserSessionRunner{})
	insp.Timeout = helperTimeout()

	mgr := systemd.NewManager(&systemd.UserRunner{})

	exec := executor.NewSystemdPodmanWithFS(mgr, insp, "", dataDir, rootlessUser, osutil.RealFileSystem{}, osutil.RealUserLookup{})
	exec.PodmanDirect = true
	exec.RestoreContainersReady = true
	return exec
}

func helperTimeout() time.Duration {
	if s := strings.TrimSpace(os.Getenv("ADMIRAL_FLEET_HELPER_TIMEOUT")); s != "" {
		if v, err := time.ParseDuration(s); err == nil && v > 0 {
			return v
		}
	}
	return 10 * time.Minute
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
