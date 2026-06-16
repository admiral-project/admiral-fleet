// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"context"
	"io"
	"os/user"
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
