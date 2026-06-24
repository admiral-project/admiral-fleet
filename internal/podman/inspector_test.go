// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"context"
	"errors"
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
	err   error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...)})
	if r.err != nil {
		return nil, r.err
	}
	if name == "podman" && len(args) > 0 && args[0] == "port" {
		return []byte("0.0.0.0:8080"), nil
	}
	if name == "podman" && len(args) > 3 && args[0] == "pod" && args[1] == "inspect" && args[3] == "--format" && args[4] == "{{.State}}" {
		return []byte("Paused"), nil
	}
	return []byte("ok"), nil
}

func (r *fakeRunner) RunWithStdin(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...)})
	if r.err != nil {
		return nil, r.err
	}
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		r.stdin = append(r.stdin, string(data))
	}
	return []byte("ok"), nil
}

type basicRunner struct {
}

func (r *basicRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
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

func TestInspectorPodIsPaused(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	inspector.Timeout = time.Second

	paused, err := inspector.PodIsPaused(context.Background(), "admiral-demo")
	if err != nil {
		t.Fatalf("pod is paused: %v", err)
	}
	if !paused {
		t.Fatal("expected pod to be paused")
	}

	expected := []call{
		{name: "podman", args: []string{"pod", "inspect", "admiral-demo", "--format", "{{.State}}"}},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("unexpected calls:\nwant: %#v\ngot:  %#v", expected, runner.calls)
	}

	// Error case
	runner.err = errors.New("fail")
	_, err = inspector.PodIsPaused(context.Background(), "admiral-demo")
	if err == nil {
		t.Error("expected error")
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

	// Error case
	runner.err = errors.New("fail")
	if err := inspector.Login(context.Background(), "r", "u", "p"); err == nil {
		t.Error("expected error")
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

	// With stdin
	if err := inspector.Login(context.Background(), "r", "u", "p"); err != nil {
		t.Fatalf("login: %v", err)
	}

	// Non-existent user
	inspector.RootlessUser = "no-such-user-hopefully-12345"
	if err := inspector.PodExists(context.Background(), "p"); err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestInspectorAllMethods(t *testing.T) {
	runner := &fakeRunner{}
	inspector := NewInspector(runner)
	ctx := context.Background()

	// PodPort
	if _, err := inspector.PodPort(ctx, "pod", "80"); err != nil {
		t.Errorf("PodPort: %v", err)
	}
	// ContainerInspect
	if _, err := inspector.ContainerInspect(ctx, "cnt"); err != nil {
		t.Errorf("ContainerInspect: %v", err)
	}
	// ContainerExists
	if err := inspector.ContainerExists(ctx, "cnt"); err != nil {
		t.Errorf("ContainerExists: %v", err)
	}
	// VolumeInspect
	if _, err := inspector.VolumeInspect(ctx, "vol"); err != nil {
		t.Errorf("VolumeInspect: %v", err)
	}
	// Exec
	if _, err := inspector.Exec(ctx, "cnt", "ls"); err != nil {
		t.Errorf("Exec: %v", err)
	}
	// ExecWithEnv
	if _, err := inspector.ExecWithEnv(ctx, "cnt", map[string]string{"FOO": "BAR"}, "ls"); err != nil {
		t.Errorf("ExecWithEnv: %v", err)
	}
	// ExecWithStdin
	if _, err := inspector.ExecWithStdin(ctx, "cnt", nil, strings.NewReader("hi"), "cat"); err != nil {
		t.Errorf("ExecWithStdin: %v", err)
	}
	// CopyToContainer
	if _, err := inspector.CopyToContainer(ctx, "/src", "cnt:/dst"); err != nil {
		t.Errorf("CopyToContainer: %v", err)
	}
	// RemoveContainer
	if err := inspector.RemoveContainer(ctx, "cnt"); err != nil {
		t.Errorf("RemoveContainer: %v", err)
	}
	// RemoveVolume
	if err := inspector.RemoveVolume(ctx, "vol"); err != nil {
		t.Errorf("RemoveVolume: %v", err)
	}

	expectedCalls := []string{
		"port", "container inspect", "container exists", "volume inspect", "exec", "exec", "exec", "cp", "rm", "volume rm",
	}
	for i, c := range runner.calls {
		if i >= len(expectedCalls) {
			break
		}
		if !strings.Contains(strings.Join(c.args, " "), expectedCalls[i]) {
			t.Errorf("Call %d: expected %q in args, got %v", i, expectedCalls[i], c.args)
		}
	}
}

func TestCommandRunner_SecurityValidation(t *testing.T) {
	cr := CommandRunner{}
	ctx := context.Background()

	// ValidateExecParams should fail for path separators in name
	_, err := cr.Run(ctx, "/bin/ls")
	if err == nil {
		t.Error("expected error for path separator in executable name")
	}

	// ValidateExecParams should fail for shell metacharacters in args
	_, err = cr.Run(ctx, "ls", "; rm -rf /")
	if err == nil {
		t.Error("expected error for shell metacharacter in arguments")
	}
}

func TestInspectorRunnerWithoutStdin(t *testing.T) {
	runner := &basicRunner{}
	inspector := NewInspector(runner)
	ctx := context.Background()

	// Podman without rootless user
	inspector.RootlessUser = ""
	err := inspector.Login(ctx, "r", "u", "p")
	if err == nil {
		t.Error("expected error because basicRunner does not support RunWithStdin")
	} else if !strings.Contains(err.Error(), "does not support stdin") {
		t.Errorf("unexpected error: %v", err)
	}

	// Podman with rootless user
	inspector.RootlessUser = "root"
	err = inspector.Login(ctx, "r", "u", "p")
	if err == nil {
		t.Error("expected error because basicRunner does not support RunWithStdin (rootless)")
	} else if !strings.Contains(err.Error(), "does not support stdin") {
		t.Errorf("unexpected error (rootless): %v", err)
	}
}
