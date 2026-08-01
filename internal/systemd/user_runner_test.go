// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package systemd

import (
	"context"
	"reflect"
	"testing"
)

func TestUserCommandArgs(t *testing.T) {
	got := userCommandArgs([]string{"stop", "admiral-demo001-db.service"})
	want := []string{"--user", "stop", "admiral-demo001-db.service"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got %v want %v", got, want)
	}
}

func TestUserRunnerRejectsShellMetacharacters(t *testing.T) {
	r := UserRunner{}
	if _, err := r.Run(context.Background(), "systemctl", "stop;rm -rf /"); err == nil {
		t.Fatal("expected error for shell metacharacter in args")
	}
}
