// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	calls     []call
	responses map[string][]error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...)})
	if r.responses != nil {
		key := name + " " + strings.Join(args, " ")
		if seq, ok := r.responses[key]; ok && len(seq) > 0 {
			err := seq[0]
			r.responses[key] = seq[1:]
			if err != nil {
				return nil, err
			}
		}
	}
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
		{name: "loginctl", args: []string{"enable-linger", "admiral-apps"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "daemon-reload"}},
		{name: "loginctl", args: []string{"enable-linger", "admiral-apps"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "start", "admiral-demo-app.service"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestManagerRootlessStartRetriesAfterMissingUnit(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]error{
			"systemd-run --wait --collect --working-directory=/tmp systemctl --machine=admiral-apps@ --user start admiral-demo-app.service": {
				errors.New("status=5/NOTINSTALLED"), nil,
			},
		},
	}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "admiral-apps"

	err := manager.Start(context.Background(), "admiral-demo-app.service")
	if err != nil {
		t.Fatalf("rootless start retry: %v", err)
	}

	expected := []call{
		{name: "loginctl", args: []string{"enable-linger", "admiral-apps"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "start", "admiral-demo-app.service"}},
		{name: "loginctl", args: []string{"enable-linger", "admiral-apps"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "daemon-reload"}},
		{name: "loginctl", args: []string{"enable-linger", "admiral-apps"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "start", "admiral-demo-app.service"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestManagerRootlessDaemonReloadEnablesLingerOnce(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "admiral-apps"

	if err := manager.DaemonReload(context.Background()); err != nil {
		t.Fatalf("daemon-reload: %v", err)
	}

	expected := []call{
		{name: "loginctl", args: []string{"enable-linger", "admiral-apps"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=admiral-apps@", "--user", "daemon-reload"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}
