// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// StartImagePuller refreshes the control-plane image list periodically and
// pulls each image through the rootless Podman runner. A failed refresh or
// pull is non-fatal: provisioning remains the source of truth and will report
// an actionable error if an image is unavailable.
func (a *Agent) StartImagePuller(ctx context.Context) {
	interval, err := time.ParseDuration(strings.TrimSpace(a.ImagePullInterval))
	if err != nil || interval <= 0 {
		interval = time.Hour
	}
	a.pullOCIImages(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pullOCIImages(ctx)
		}
	}
}

func (a *Agent) pullOCIImages(ctx context.Context) {
	images, err := a.fetchOCIImages(ctx)
	if err != nil {
		slog.Warn("failed to refresh OCI image list", "error", err, "node_id", a.NodeID)
		return
	}
	for _, image := range images {
		if _, err := a.runPodman(ctx, "pull", image); err != nil {
			slog.Warn("failed to pre-pull OCI image", "image", image, "error", err, "node_id", a.NodeID)
			continue
		}
		slog.Info("pre-pulled OCI image", "image", image, "node_id", a.NodeID)
	}
}

func (a *Agent) fetchOCIImages(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.APIURL+"/api/v1/fleet/oci_images", nil)
	if err != nil {
		return nil, fmt.Errorf("create OCI image request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.FleetToken)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OCI image list: %w", err)
	}
	defer drainAndCloseResponse(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch OCI image list returned HTTP %d", resp.StatusCode)
	}
	var images []string
	if err := json.NewDecoder(resp.Body).Decode(&images); err != nil {
		return nil, fmt.Errorf("decode OCI image list: %w", err)
	}
	return images, nil
}
