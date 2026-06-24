// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"testing"
	"time"
)

func TestLeaseRefreshInterval(t *testing.T) {
	tests := []struct {
		name  string
		lease time.Duration
		want  time.Duration
	}{
		{name: "short leases keep minimum cadence", lease: 30 * time.Second, want: 10 * time.Second},
		{name: "very short leases clamp to five seconds", lease: 9 * time.Second, want: 5 * time.Second},
		{name: "long leases renew at one third", lease: 6 * time.Minute, want: 2 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := leaseRefreshInterval(tt.lease)
			if got != tt.want {
				t.Fatalf("leaseRefreshInterval(%v) = %v, want %v", tt.lease, got, tt.want)
			}
		})
	}
}

func TestVerifyTaskNoKey(t *testing.T) {
	c := &Consumer{}
	cmd := &claimedCommand{id: "t1"}
	if err := c.verifyTask(cmd); err != nil {
		t.Fatalf("verifyTask should return nil if no public key: %v", err)
	}
}

func TestOpenPayloadNoKey(t *testing.T) {
	c := &Consumer{}
	_, err := c.openPayload("anything")
	if err == nil {
		t.Fatal("expected error when no encryption key")
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},
		{1, 2 * time.Second},
		{2, 5 * time.Second},
		{3, 10 * time.Second},
		{4, 30 * time.Second},
		{10, 30 * time.Second},
	}
	for _, tt := range tests {
		got := backoff(tt.attempt)
		if got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}
