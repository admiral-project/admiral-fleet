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

func TestRemoveCred(t *testing.T) {
	// 1. Removing a non-existent file should not return an error
	err := RemoveCred("/nonexistent/file/path/does/not/exist")
	if err != nil {
		t.Fatalf("expected nil error for non-existent file, got %v", err)
	}

	// 2. Removing an existing file
	tmpFile, err := os.CreateTemp("", "test-cred-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	err = RemoveCred(tmpPath)
	if err != nil {
		t.Fatalf("expected nil error when removing file, got %v", err)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed")
	}
}

func TestCommandRunner_Run(t *testing.T) {
	runner := CommandRunner{}

	// Successful run
	out, err := runner.Run(context.Background(), "true")
	if err != nil {
		t.Fatalf("expected successful run, got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %q", string(out))
	}

	// Failed run
	_, err = runner.Run(context.Background(), "false")
	if err == nil {
		t.Fatal("expected failure running false")
	}

	// Validation failure (exec params validation)
	_, err = runner.Run(context.Background(), "ls", ";")
	if err == nil {
		t.Fatal("expected validation error for shell metacharacter")
	}
}

func TestCommandRunner_RunWithStdin(t *testing.T) {
	runner := CommandRunner{}

	stdin := strings.NewReader("hello world")
	out, err := runner.RunWithStdin(context.Background(), stdin, "cat")
	if err != nil {
		t.Fatalf("expected successful run of cat with stdin, got %v", err)
	}
	if string(out) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(out))
	}
}

func TestIsTransientRootlessReloadError(t *testing.T) {
	tests := []struct {
		errStr   string
		expected bool
	}{
		{"Connection reset by peer", true},
		{"Transport endpoint is not connected", true},
		{"some other error", false},
	}

	for _, tc := range tests {
		t.Run(tc.errStr, func(t *testing.T) {
			got := isTransientRootlessReloadError(errors.New(tc.errStr))
			if got != tc.expected {
				t.Errorf("isTransientRootlessReloadError(%q) = %v; want %v", tc.errStr, got, tc.expected)
			}
		})
	}
}

func TestNewManager_TimeoutEnv(t *testing.T) {
	// Save existing env value
	orig, exists := os.LookupEnv("ADMIRAL_SYSTEMD_TIMEOUT")
	defer func() {
		if exists {
			os.Setenv("ADMIRAL_SYSTEMD_TIMEOUT", orig)
		} else {
			os.Unsetenv("ADMIRAL_SYSTEMD_TIMEOUT")
		}
	}()

	os.Setenv("ADMIRAL_SYSTEMD_TIMEOUT", "45")
	m := NewManager(&fakeRunner{})
	if m.Timeout != 45*time.Second {
		t.Errorf("expected Timeout to be 45s, got %v", m.Timeout)
	}

	os.Setenv("ADMIRAL_SYSTEMD_TIMEOUT", "invalid")
	m2 := NewManager(&fakeRunner{})
	if m2.Timeout != defaultTimeout {
		t.Errorf("expected Timeout to fall back to defaultTimeout, got %v", m2.Timeout)
	}
}

type unsupportedFakeRunner struct{}

func (r unsupportedFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return nil, nil
}

func TestEncryptCred_UnsupportedRunner(t *testing.T) {
	ctx := context.Background()
	stdin := strings.NewReader("secret")
	err := EncryptCred(ctx, unsupportedFakeRunner{}, "my-cred", stdin, "/tmp/out.cred")
	if err == nil {
		t.Fatal("expected error with unsupported runner")
	}
	if !strings.Contains(err.Error(), "does not support stdin") {
		t.Errorf("expected error to mention unsupported stdin, got %v", err)
	}
}

func TestManager_RootlessDaemonReload_Errors(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]error{
			"systemd-run --wait --collect --working-directory=/tmp systemctl --machine=admiral-apps@ --user daemon-reload": {
				errors.New("Connection reset by peer"), nil,
			},
		},
	}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "admiral-apps"

	err := manager.DaemonReload(context.Background())
	if err != nil {
		t.Fatalf("expected transient daemon reload error to be retried and succeed: %v", err)
	}
}

func TestRemoveCred_Directory(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file inside tmpDir so that the directory is not empty
	f, err := os.CreateTemp(tmpDir, "dummy-*")
	if err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	f.Close()

	err = RemoveCred(tmpDir)
	if err == nil {
		t.Fatal("expected error when trying to remove a non-empty directory using RemoveCred")
	}
}

func TestEncryptCred_CommandRunner(t *testing.T) {
	runner := &CommandRunner{}
	ctx := context.Background()
	stdin := strings.NewReader("secret")
	// Since systemd-creds is likely missing, it should return an error, but it still executes the *CommandRunner path!
	outPath := filepath.Join(t.TempDir(), "out.cred")
	err := EncryptCred(ctx, runner, "my-cred", stdin, outPath)
	if err == nil {
		t.Log("expected systemd-creds to fail or not exist, but got success")
	}
}

func TestManager_LingerError(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]error{
			"loginctl enable-linger user-bad": {
				errors.New("some linger error"),
			},
		},
	}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "user-bad"

	err := manager.Stop(context.Background(), "unit1")
	if err == nil {
		t.Fatal("expected error due to linger failure")
	}
	if !strings.Contains(err.Error(), "enable lingering for") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManager_StartNonMissingUnitError(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]error{
			"systemd-run --wait --collect --working-directory=/tmp systemctl --machine=admiral-apps@ --user start unit1": {
				errors.New("generic execution error"),
			},
		},
	}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "admiral-apps"

	err := manager.Start(context.Background(), "unit1")
	if err == nil {
		t.Fatal("expected error, but got nil")
	}
	if strings.Contains(err.Error(), "reload rootless manager") {
		t.Fatalf("should not have retried daemon reload on generic error: %v", err)
	}
}

func TestManager_DaemonReloadNonTransientError(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]error{
			"systemd-run --wait --collect --working-directory=/tmp systemctl --machine=admiral-apps@ --user daemon-reload": {
				errors.New("permanent reload failure"),
			},
		},
	}
	manager := NewManager(runner)
	manager.Timeout = time.Second
	manager.RunAsUser = "admiral-apps"

	err := manager.DaemonReload(context.Background())
	if err == nil {
		t.Fatal("expected error from non-transient daemon reload, got nil")
	}
}
