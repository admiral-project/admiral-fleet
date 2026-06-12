// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []call
}

func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...)})
	return []byte("ok"), nil
}

func TestManagerUsesSystemctlArgumentArrays(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	manager.Timeout = time.Second

	if err := manager.DaemonReload(context.Background()); err != nil {
		t.Fatalf("daemon-reload: %v", err)
	}
	if err := manager.Start(context.Background(), "admiral-demo-app.service"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := manager.Status(context.Background(), "admiral-demo-app.service"); err != nil {
		t.Fatalf("status: %v", err)
	}

	expected := []call{
		{name: "systemctl", args: []string{"daemon-reload"}},
		{name: "systemctl", args: []string{"start", "admiral-demo-app.service"}},
		{name: "systemctl", args: []string{"status", "--no-pager", "admiral-demo-app.service"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestManagerUsesSystemdRunForRootlessUserManager(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "admiral-apps"

	if err := manager.DaemonReload(context.Background()); err != nil {
		t.Fatalf("daemon-reload: %v", err)
	}
	if err := manager.Start(context.Background(), "admiral-demo-app.service"); err != nil {
		t.Fatalf("start: %v", err)
	}

	expected := []call{
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "daemon-reload"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "start", "admiral-demo-app.service"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}
