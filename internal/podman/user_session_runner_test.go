// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"context"
	"reflect"
	"testing"
)

func TestUserSessionCommandArgs(t *testing.T) {
	got := userSessionCommandArgs("podman", []string{"exec", "admiral-demo001-db", "pg_isready"})
	want := []string{"--user", "--wait", "--collect", "--pipe", "--", "podman", "exec", "admiral-demo001-db", "pg_isready"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got %v want %v", got, want)
	}
}

func TestUserSessionRunnerRejectsShellMetacharacters(t *testing.T) {
	r := UserSessionRunner{}
	if _, err := r.Run(context.Background(), "podman", "exec;rm -rf /"); err == nil {
		t.Fatal("expected error for shell metacharacter in args")
	}
	if _, err := r.Run(context.Background(), "podman", "exec", "x;ls"); err == nil {
		t.Fatal("expected error for shell metacharacter in args")
	}
	if _, err := r.Run(context.Background(), "bad/path", "exec"); err == nil {
		t.Fatal("expected error for path separator in executable name")
	}
}
