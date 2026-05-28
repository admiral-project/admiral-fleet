package agent

import (
	"encoding/json"
	"log"
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

func StartHTTPServer(addr, nodeID, executor, targetHost, targetPort string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/endpoint", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, EndpointInfo{
			NodeID:     nodeID,
			TargetHost: targetHost,
			TargetPort: targetPort,
			Executor:   executor,
			Status:     "healthy",
			CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	})

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("admiral-fleet internal http server stopped: %v", err)
		}
	}()
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
