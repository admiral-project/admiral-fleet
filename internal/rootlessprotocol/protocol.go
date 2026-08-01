// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

// Package rootlessprotocol defines the private stdin/stdout contract shared by
// Fleet and its rootless Podman helpers.
package rootlessprotocol

const Version = 1

type Request struct {
	Version int      `json:"version"`
	Name    string   `json:"name"`
	Args    []string `json:"args"`
	Stdin   []byte   `json:"stdin,omitempty"`
	Trusted bool     `json:"trusted,omitempty"`
}

type Response struct {
	Version int    `json:"version"`
	Stdout  []byte `json:"stdout,omitempty"`
	Error   string `json:"error,omitempty"`
}
