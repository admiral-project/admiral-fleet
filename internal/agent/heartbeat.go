// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

const FleetVersion = "0.0.1alpha6"

func (a *Agent) StartHeartbeatSender(ctx context.Context) {
	time.Sleep(10 * time.Second)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sendHeartbeat(ctx)
		}
	}
}

func (a *Agent) sendHeartbeat(ctx context.Context) {
	req := a.buildHeartbeat(ctx)
	if err := a.postHeartbeat(req); err != nil {
		slog.Warn("heartbeat error", "error", err, "node_id", a.NodeID)
	}
}

func (a *Agent) buildHeartbeat(ctx context.Context) admiral.HeartbeatRequest {
	hostname, _ := os.Hostname()
	ip := detectLocalIP()
	podmanV := detectPodmanVersion(ctx)
	diskTotal, diskUsed := detectDiskUsage(ctx)
	ramTotal, ramUsed, ramAvail := detectRAMUsage(ctx)
	podsActive, podsPaused, podsFailed := a.countPodsByStatus(ctx)

	return admiral.HeartbeatRequest{
		NodeID:        a.NodeID,
		Hostname:      hostname,
		IP:            ip,
		PodmanVersion: podmanV,
		FleetVersion:  FleetVersion,
		Status:        "active",
		DiskTotal:     diskTotal,
		DiskUsed:      diskUsed,
		RAMTotal:      ramTotal,
		RAMUsed:       ramUsed,
		RAMAvailable:  ramAvail,
		PodsActive:    podsActive,
		PodsPaused:    podsPaused,
		PodsFailed:    podsFailed,
	}
}

func (a *Agent) postHeartbeat(req admiral.HeartbeatRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, a.APIURL+"/api/v1/nodes/heartbeat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create heartbeat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Admiral-Token", a.SharedToken)

	resp, err := a.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat failed with HTTP %d", resp.StatusCode)
	}
	return nil
}

func detectLocalIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

func detectPodmanVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "podman", "version", "--format", "{{.Version}}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectDiskUsage(ctx context.Context) (totalBytes, usedBytes int64) {
	cmd := exec.CommandContext(ctx, "df", "-B1", "--output=size,used", "/")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return 0, 0
	}
	total, _ := strconv.ParseInt(fields[0], 10, 64)
	used, _ := strconv.ParseInt(fields[1], 10, 64)
	return total, used
}

func detectRAMUsage(ctx context.Context) (totalBytes, usedBytes, availBytes int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	var total, avail int64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				total, _ = strconv.ParseInt(fields[1], 10, 64)
				total *= 1024 // kB to bytes
			}
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				avail, _ = strconv.ParseInt(fields[1], 10, 64)
				avail *= 1024
			}
		}
	}
	if total > 0 {
		used := total - avail
		if used < 0 {
			used = 0
		}
		return total, used, avail
	}
	return 0, 0, 0
}

func (a *Agent) countPodsByStatus(ctx context.Context) (active, paused, failed int) {
	pods, err := a.listAdmiralPods(ctx)
	if err != nil {
		return 0, 0, 0
	}
	for _, pod := range pods {
		switch pod.Status {
		case "Running":
			active++
		case "Paused":
			paused++
		case "Exited", "Stopped", "Error", "Dead":
			failed++
		}
	}
	return active, paused, failed
}
