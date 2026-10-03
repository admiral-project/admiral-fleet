// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"net"
	"net/url"
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

func TestResolveTrustedRestoreOriginAllowsOnlyExactPinnedHarborCapability(t *testing.T) {
	const origin = "https://localhost:5001"
	const prefix = "/api/v1/backups/uploads/"
	goodURL := "https://localhost:5001/api/v1/backups/uploads/upbk_123/download?customer_id=cus_123&expires=2000000000&signature=abc"
	allowed := []string{"::1", "127.0.0.1"}

	parsed, err := url.Parse(goodURL)
	if err != nil {
		t.Fatal(err)
	}
	ips, err := resolveTrustedRestoreOrigin(context.Background(), parsed, origin, prefix, allowed)
	if err != nil {
		t.Fatalf("expected signed local Harbor origin to be allowed: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("expected pinned loopback addresses")
	}

	for _, tc := range []struct {
		name   string
		uri    string
		origin string
		path   string
		ips    []string
	}{
		{name: "wrong port", uri: "https://localhost:5002/api/v1/backups/uploads/upbk_123/download", origin: origin, path: prefix, ips: allowed},
		{name: "wrong path", uri: "https://localhost:5001/admin", origin: origin, path: prefix, ips: allowed},
		{name: "extra path segment", uri: "https://localhost:5001/api/v1/backups/uploads/upbk_123/extra/download?customer_id=cus_123&expires=2000000000&signature=abc", origin: origin, path: prefix, ips: allowed},
		{name: "encoded path separator", uri: "https://localhost:5001/api/v1/backups/uploads/upbk_123%2F..%2Fadmin/download?customer_id=cus_123&expires=2000000000&signature=abc", origin: origin, path: prefix, ips: allowed},
		{name: "cross-origin host", uri: "https://127.0.0.1:5001/api/v1/backups/uploads/upbk_123/download", origin: origin, path: prefix, ips: allowed},
		{name: "unapproved resolved IP", uri: goodURL, origin: origin, path: prefix, ips: []string{"127.0.0.2"}},
		{name: "missing address capability", uri: goodURL, origin: origin, path: prefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := url.Parse(tc.uri)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resolveTrustedRestoreOrigin(context.Background(), parsed, tc.origin, tc.path, tc.ips); err == nil {
				t.Fatal("expected untrusted restore URL to be rejected")
			}
		})
	}
}
