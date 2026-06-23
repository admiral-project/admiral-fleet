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

type ReadyInfo struct {
	Status    string `json:"status"`
	NodeID    string `json:"node_id,omitempty"`
	Executor  string `json:"executor,omitempty"`
	CheckedAt string `json:"checked_at"`
}

func ipAllowed(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// Without VPN, we only trust the local node.
	return ip.IsLoopback()
}

func allowedHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ipAllowed(r.RemoteAddr) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"forbidden"}`))
			return
		}
		h(w, r)
	}
}

func StartHTTPServer(addr, nodeID, executor, targetHost, targetPort string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", allowedHandler(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	}))
	mux.HandleFunc("/endpoint", allowedHandler(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	}))
	mux.HandleFunc("/ready", allowedHandler(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, ReadyInfo{
			Status:    "ok",
			NodeID:    nodeID,
			Executor:  executor,
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}))

	// Listen on all interfaces; IP filtering is done in the handlers
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
	bindAddr := net.JoinHostPort("", port)
	server := &http.Server{
		Addr:              bindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		slog.Info("starting internal HTTP server", "addr", bindAddr)
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
