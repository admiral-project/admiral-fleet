// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	stdin     []string
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

func (r *fakeRunner) RunWithStdin(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
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

func TestManagerAllMethods(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	ctx := context.Background()

	_ = manager.Stop(ctx, "unit")
	_ = manager.Restart(ctx, "unit")
	_ = manager.Enable(ctx, "unit")
	_ = manager.Disable(ctx, "unit")
	_ = manager.ResetFailed(ctx)

	expected := []call{
		{name: "systemctl", args: []string{"stop", "unit"}},
		{name: "systemctl", args: []string{"restart", "unit"}},
		{name: "systemctl", args: []string{"enable", "unit"}},
		{name: "systemctl", args: []string{"disable", "unit"}},
		{name: "systemctl", args: []string{"reset-failed"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestManagerRootlessReloadRetry(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]error{
			"systemd-run --wait --collect --working-directory=/tmp systemctl --machine=admiral-apps@ --user daemon-reload": {
				errors.New("Connection reset by peer"), nil,
			},
		},
	}
	manager := NewManager(runner)
	manager.RunAsUser = "admiral-apps"

	if err := manager.DaemonReload(context.Background()); err != nil {
		t.Fatalf("daemon-reload retry: %v", err)
	}
}

func TestCommandRunner(t *testing.T) {
	cr := CommandRunner{}
	ctx := context.Background()
	_, _ = cr.Run(ctx, "true")
	_, _ = cr.RunWithStdin(ctx, strings.NewReader("hi"), "cat")
}

func TestEncryptCred(t *testing.T) {
	runner := &fakeRunner{}
	ctx := context.Background()
	err := EncryptCred(ctx, runner, "name", strings.NewReader("secret"), "/tmp/out")
	if err != nil {
		t.Fatalf("EncryptCred: %v", err)
	}
}

func TestRemoveCred(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cred")
	_ = os.WriteFile(path, []byte("data"), 0600)
	if err := RemoveCred(path); err != nil {
		t.Fatalf("RemoveCred: %v", err)
	}
	if err := RemoveCred(path); err != nil {
		t.Fatalf("RemoveCred (not exist): %v", err)
	}
}

func TestCredPaths(t *testing.T) {
	dir := CredDir("/var/lib/admiral", "inst")
	if !strings.Contains(dir, "inst") {
		t.Errorf("CredDir: %s", dir)
	}
	path := CredFilePath("/var/lib/admiral", "inst", "svc", "env")
	if !strings.Contains(path, "svc-env") {
		t.Errorf("CredFilePath: %s", path)
	}
}
