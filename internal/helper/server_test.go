// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package helper

import "testing"

func TestFirstArg(t *testing.T) {
	if got := firstArg(nil); got != "" {
		t.Fatalf("firstArg(nil) = %q", got)
	}
	if got := firstArg([]string{"exec", "container"}); got != "exec" {
		t.Fatalf("firstArg() = %q", got)
	}
}
