// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package healthcheck

import "github.com/admiral-project/admiral/admirald/pkg/admiral"

// Check is the non-secret local metadata Fleet keeps to repeat a declared
// service health check after provisioning has completed.
type Check struct {
	ServiceName string                  `json:"service_name"`
	HealthCheck admiral.YAMLHealthCheck `json:"healthcheck"`
	HostAddress string                  `json:"host_address,omitempty"`
	HostPort    int                     `json:"host_port,omitempty"`
}
