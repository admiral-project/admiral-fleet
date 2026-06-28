// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"context"
	"errors"
	"io"
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

func (r *fakeRunner) RunWithStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
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

func TestManagerStopRestartEnableDisable(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	manager.Timeout = time.Second

	ctx := context.Background()
	unit := "admiral-test.service"

	if err := manager.Stop(ctx, unit); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := manager.Restart(ctx, unit); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if err := manager.Enable(ctx, unit); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := manager.Disable(ctx, unit); err != nil {
		t.Fatalf("disable: %v", err)
	}

	expected := []call{
		{name: "systemctl", args: []string{"stop", unit}},
		{name: "systemctl", args: []string{"restart", unit}},
		{name: "systemctl", args: []string{"enable", unit}},
		{name: "systemctl", args: []string{"disable", unit}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestManagerResetFailed(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	manager.Timeout = time.Second

	if err := manager.ResetFailed(context.Background()); err != nil {
		t.Fatalf("reset-failed: %v", err)
	}

	expected := []call{
		{name: "systemctl", args: []string{"reset-failed"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestManagerRootlessStop(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "user1"

	if err := manager.Stop(context.Background(), "unit1"); err != nil {
		t.Fatalf("rootless stop: %v", err)
	}

	expected := []call{
		{name: "loginctl", args: []string{"enable-linger", "user1"}},
		{name: "systemd-run", args: []string{"--wait", "--collect", "--working-directory=/tmp", "systemctl", "--machine=user1@", "--user", "stop", "unit1"}},
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

func TestEncryptCred(t *testing.T) {
	runner := &fakeRunner{}
	ctx := context.Background()
	stdin := strings.NewReader("secret")
	err := EncryptCred(ctx, runner, "my-cred", stdin, "/tmp/out.cred")
	if err != nil {
		t.Fatalf("EncryptCred: %v", err)
	}

	expected := call{name: "systemd-creds", args: []string{"encrypt", "--name=my-cred", "-", "/tmp/out.cred"}}
	if !reflect.DeepEqual(runner.calls[0], expected) {
		t.Fatalf("unexpected call: %#v", runner.calls[0])
	}
}

func TestCredPathHelpers(t *testing.T) {
	dir := CredDir("/var/lib/admiral", "inst1")
	if dir != "/var/lib/admiral/instances/inst1/creds" {
		t.Errorf("CredDir: %s", dir)
	}
	path := CredFilePath("/var/lib/admiral", "inst1", "svc1", "PASS")
	if path != "/var/lib/admiral/instances/inst1/creds/svc1-PASS.cred" {
		t.Errorf("CredFilePath: %s", path)
	}
}
