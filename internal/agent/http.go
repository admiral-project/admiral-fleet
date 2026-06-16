// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type EndpointInfo struct {
	NodeID     string `json:"node_id"`
	TargetHost string `json:"target_host,omitempty"`
	TargetPort string `json:"target_port,omitempty"`
	Executor   string `json:"executor"`
	Status     string `json:"status"`
	CheckedAt  string `json:"checked_at"`
}

func StartHTTPServer(addr, nodeID, executor, targetHost, targetPort string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/endpoint", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Restrict to localhost to avoid exposing internal API on the network
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		if !strings.Contains(addr, ":") && addr != "" {
			port = addr
		} else {
			slog.Warn("internal HTTP server address is invalid; skipping local listener", "addr", addr, "error", err)
			return
		}
	}
	if port == "" {
		slog.Warn("internal HTTP server address is missing a port; skipping local listener", "addr", addr)
		return
	}
	localAddr := net.JoinHostPort("127.0.0.1", port)
	server := &http.Server{
		Addr:              localAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		slog.Info("starting internal HTTP server", "addr", localAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("internal HTTP server stopped", "error", err)
		}
	}()
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("json encode failed", "error", err)
	}
}
