// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import "testing"

func TestRemoteRunnerSelectsSpecializedHelper(t *testing.T) {
	runner := RemoteRunner{Lifecycle: "/lifecycle", Setup: "/setup"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "inspect", args: []string{"container", "inspect", "admiral-demo"}, want: "/lifecycle"},
		{name: "remove", args: []string{"rm", "--force", "admiral-demo"}, want: "/lifecycle"},
		{name: "pull", args: []string{"pull", "registry.example/app:latest"}, want: "/lifecycle"},
		{name: "rootless restore", args: []string{"unshare", "tar", "--extract"}, want: "/lifecycle"},
		{name: "exec", args: []string{"exec", "admiral-demo", "true"}, want: "/setup"},
		{name: "trusted run", args: []string{"run", "--rm", "image", "sh", "-c", "echo ok"}, want: "/setup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runner.helperFor(tt.args)
			if err != nil {
				t.Fatalf("helperFor() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("helperFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoteRunnerRejectsUnsupportedOperation(t *testing.T) {
	_, err := (RemoteRunner{}).helperFor([]string{"build", "image"})
	if err == nil {
		t.Fatal("expected unsupported operation error")
	}
}

func TestRemoteRunnerRejectsNonPodmanExecutable(t *testing.T) {
	_, err := (RemoteRunner{}).Run(nil, "sh", "-c", "id")
	if err == nil {
		t.Fatal("expected executable validation error")
	}
}
