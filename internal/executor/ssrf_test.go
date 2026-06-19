// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import "net"

import (
	"testing"
)

func TestIsPrivateIPInternal(t *testing.T) {
	tests := []struct {
		ip      string
		want    bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"8.8.8.8", false},
		{"100.64.0.1", true},       // CGNAT
		{"192.0.2.1", true},        // Documentation
		{"198.51.100.1", true},     // Documentation
		{"203.0.113.1", true},      // Documentation
		{"0.0.0.0", true},          // Unspecified
		{"::1", true},              // IPv6 Loopback
		{"fe80::1", true},          // IPv6 Link-local
		{"2002::1", true},          // 6to4 Relay
		{"2001:db8::1", true},      // Documentation IPv6 (IsPrivate handles this)
		{"2001:0::1", true},        // Teredo
		{"2001:20::1", true},       // ORCHIDv2
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if got := isPrivateIP(ip); got != tt.want {
				t.Errorf("isPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsPrivateHost(t *testing.T) {
	tests := []struct {
		host    string
		wantErr bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"8.8.8.8", false},
		{"google.com", false},
		{"localhost", true},
		{"100.64.0.1", true},       // CGNAT
		{"192.0.2.1", true},        // Documentation
		{"198.51.100.1", true},     // Documentation
		{"203.0.113.1", true},      // Documentation
		{"0.0.0.0", true},          // Unspecified
		{"::1", true},              // IPv6 Loopback
		{"fe80::1", true},          // IPv6 Link-local
		{"2002::1", true},          // 6to4 Relay
		{"2001:db8::1", false},      // Documentation IPv6 (IsPrivate handles this usually)
		{"2001:0::1", true},        // Teredo
		{"2001:20::1", true},       // ORCHIDv2
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			err := isPrivateHost(tt.host)
			if (err != nil) != tt.wantErr {
				t.Errorf("isPrivateHost(%q) error = %v, wantErr %v", tt.host, err, tt.wantErr)
			}
		})
	}
}
