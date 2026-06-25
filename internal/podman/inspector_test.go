// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"context"
	"io"
	"os/user"
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
	calls []call
	stdin []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...)})
	return []byte("ok"), nil
}

func (r *fakeRunner) RunWithStdin(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...)})
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		r.stdin = append(r.stdin, string(data))
	}
	return []byte("ok"), nil
}

func TestInspectorUsesPodmanArgumentArrays(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if err := inspector.PodExists(context.Background(), "admiral-demo"); err != nil {
		t.Fatalf("pod exists: %v", err)
	}
	if _, err := inspector.PodPS(context.Background()); err != nil {
		t.Fatalf("pod ps: %v", err)
	}
	if _, err := inspector.ContainerPS(context.Background()); err != nil {
		t.Fatalf("container ps: %v", err)
	}
	if err := inspector.RemovePod(context.Background(), "admiral-demo"); err != nil {
		t.Fatalf("remove pod: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"pod", "exists", "admiral-demo"}},
		{name: "podman", args: []string{"pod", "ps", "--format", "json"}},
		{name: "podman", args: []string{"ps", "--format", "json"}},
		{name: "podman", args: []string{"pod", "rm", "--force", "admiral-demo"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorExecWithEnv(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	env := map[string]string{"KEY": "VALUE"}
	if _, err := inspector.ExecWithEnv(context.Background(), "my-container", env, "ls"); err != nil {
		t.Fatalf("exec with env: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "podman" {
		t.Fatalf("expected podman, got %s", call.name)
	}
	// Check that --env-file is present
	foundEnvFile := false
	for i, arg := range call.args {
		if arg == "--env-file" {
			foundEnvFile = true
			if i+1 >= len(call.args) {
				t.Fatal("--env-file has no argument")
			}
			break
		}
	}
	if !foundEnvFile {
		t.Fatal("--env-file not found in podman exec args")
	}
}

func TestInspectorExecWithStdin(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	stdin := strings.NewReader("input data")
	if _, err := inspector.ExecWithStdin(context.Background(), "my-container", nil, stdin, "cat"); err != nil {
		t.Fatalf("exec with stdin: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	if runner.calls[0].args[0] != "exec" {
		t.Fatalf("expected exec, got %s", runner.calls[0].args[0])
	}
	// Should have -i
	foundI := false
	for _, arg := range runner.calls[0].args {
		if arg == "-i" {
			foundI = true
			break
		}
	}
	if !foundI {
		t.Fatal("-i not found in podman exec args")
	}
	if len(runner.stdin) != 1 || runner.stdin[0] != "input data" {
		t.Fatalf("unexpected stdin: %v", runner.stdin)
	}
}

func TestInspectorExecTrustedShell(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if _, err := inspector.ExecTrustedShell(context.Background(), "my-container", "wp core is-installed || wp core install"); err != nil {
		t.Fatalf("exec trusted shell: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	expected := call{name: "podman", args: []string{"exec", "my-container", "sh", "-c", "wp core is-installed || wp core install"}}
	if !reflect.DeepEqual(runner.calls[0], expected) {
		t.Fatalf("unexpected call:\nwant: %#v\ngot:  %#v", expected, runner.calls[0])
	}
}

func TestInspectorRunTrustedShellInPodUsesContainerUser(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	env := map[string]string{"APP_USER": "admin"}
	mounts := []string{"admiral-demo-web:/data"}
	if _, err := inspector.RunTrustedShellInPod(context.Background(), "admiral-demo", "docker.io/gitea/gitea:1.22", env, mounts, "1000", "gitea migrate"); err != nil {
		t.Fatalf("run trusted shell in pod: %v", err)
	}

	expected := []call{
		{
			name: "podman",
			args: []string{
				"run", "--rm", "--pod", "admiral-demo",
				"--user", "1000",
				"--env", "APP_USER=admin",
				"-v", "admiral-demo-web:/data",
				"docker.io/gitea/gitea:1.22",
				"sh", "-c", "gitea migrate",
			},
		},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorVolumeMethods(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if _, err := inspector.VolumeInspect(context.Background(), "my-vol"); err != nil {
		t.Fatalf("volume inspect: %v", err)
	}
	if err := inspector.RemoveVolume(context.Background(), "my-vol"); err != nil {
		t.Fatalf("remove volume: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"volume", "inspect", "my-vol", "--format", "json"}},
		{name: "podman", args: []string{"volume", "rm", "--force", "my-vol"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorContainerMethods(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if err := inspector.ContainerExists(context.Background(), "my-cont"); err != nil {
		t.Fatalf("container exists: %v", err)
	}
	if _, err := inspector.ContainerInspect(context.Background(), "my-cont"); err != nil {
		t.Fatalf("container inspect: %v", err)
	}
	if err := inspector.RemoveContainer(context.Background(), "my-cont"); err != nil {
		t.Fatalf("remove container: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"container", "exists", "my-cont"}},
		{name: "podman", args: []string{"container", "inspect", "my-cont", "--format", "json"}},
		{name: "podman", args: []string{"rm", "--force", "my-cont"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorPodPort(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if _, err := inspector.PodPort(context.Background(), "my-pod", "80"); err != nil {
		t.Fatalf("pod port: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"port", "my-pod", "80"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorCopyToContainer(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if _, err := inspector.CopyToContainer(context.Background(), "/src", "my-cont:/dst"); err != nil {
		t.Fatalf("copy to container: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"cp", "/src", "my-cont:/dst"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorPodIsPaused(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	paused, err := inspector.PodIsPaused(context.Background(), "admiral-demo")
	if err != nil {
		t.Fatalf("pod is paused: %v", err)
	}
	if paused {
		t.Fatal("expected pod not to be paused")
	}

	expected := []call{
		{name: "podman", args: []string{"pod", "inspect", "admiral-demo", "--format", "{{.State}}"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorPauseUnpausePod(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if err := inspector.PodPause(context.Background(), "admiral-demo"); err != nil {
		t.Fatalf("pod pause: %v", err)
	}
	if err := inspector.PodUnpause(context.Background(), "admiral-demo"); err != nil {
		t.Fatalf("pod unpause: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"pod", "pause", "admiral-demo"}},
		{name: "podman", args: []string{"pod", "unpause", "admiral-demo"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}

func TestInspectorLoginUsesPasswordStdin(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	if err := inspector.Login(context.Background(), "registry.example.com", "demo", "super-secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	expected := []call{
		{name: "podman", args: []string{"login", "--username", "demo", "--password-stdin", "registry.example.com"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
	if len(runner.stdin) != 1 || runner.stdin[0] != "super-secret" {
		t.Fatalf("unexpected stdin payloads: %#v", runner.stdin)
	}
}

func TestInspectorUsesRunuserForRootlessPodman(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	inspector.RootlessUser = currentUser.Username

	if err := inspector.PodExists(context.Background(), "admiral-demo"); err != nil {
		t.Fatalf("pod exists: %v", err)
	}

	expected := []call{
		{name: "runuser", args: []string{"-u", currentUser.Username, "--", "env", "XDG_RUNTIME_DIR=/run/user/" + currentUser.Uid, "podman", "pod", "exists", "admiral-demo"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}
}
