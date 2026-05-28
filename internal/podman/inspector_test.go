package podman

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
