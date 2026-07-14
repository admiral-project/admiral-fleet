// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
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

func ipAllowed(addr, allowedAdminIP string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	adminIP := net.ParseIP(allowedAdminIP)
	return adminIP != nil && ip.Equal(adminIP)
}

func allowedHandler(allowedAdminIP string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ipAllowed(r.RemoteAddr, allowedAdminIP) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"forbidden"}`))
			return
		}
		h(w, r)
	}
}

func StartHTTPServer(addr, nodeID, executor, targetHost, targetPort string) {
	StartHTTPServerWithAllowedAdmin(addr, nodeID, executor, targetHost, targetPort, "")
}

func StartHTTPServerWithAllowedAdmin(addr, nodeID, executor, targetHost, targetPort, allowedAdminIP string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", allowedHandler(allowedAdminIP, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	}))
	mux.HandleFunc("/endpoint", allowedHandler(allowedAdminIP, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	}))
	mux.HandleFunc("/ready", allowedHandler(allowedAdminIP, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, ReadyInfo{
			Status:    "ok",
			NodeID:    nodeID,
			Executor:  executor,
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}))

	bindAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		slog.Warn("internal HTTP server address is invalid; skipping local listener", "addr", addr, "error", err)
		return
	}
	server := &http.Server{
		Addr:              bindAddr.String(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		slog.Info("starting internal HTTP server", "addr", bindAddr.String())
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
