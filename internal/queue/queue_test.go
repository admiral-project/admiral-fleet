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
