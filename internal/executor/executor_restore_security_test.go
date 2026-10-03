// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"net"
	"testing"
)

func TestResolveRestoreHostAllowsOnlyExplicitTrustedPrivateIP(t *testing.T) {
	const trusted = "10.99.0.100"

	ips, err := resolveRestoreHost(context.Background(), trusted, trusted)
	if err != nil {
		t.Fatalf("expected registered portal IP to be allowed, got %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP(trusted)) {
		t.Fatalf("unexpected resolved addresses: %v", ips)
	}

	for _, tc := range []struct {
		name            string
		host            string
		trustedSourceIP string
	}{
		{name: "different private IP", host: "10.99.0.101", trustedSourceIP: trusted},
		{name: "private IP without trust", host: trusted},
		{name: "loopback cannot be trusted", host: "127.0.0.1", trustedSourceIP: "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveRestoreHost(context.Background(), tc.host, tc.trustedSourceIP); err == nil {
				t.Fatalf("expected %s to be rejected", tc.host)
			}
		})
	}
}
