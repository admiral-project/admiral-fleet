// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/admiral-project/admiral/admiral-fleet/internal/helper"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := helper.Serve(context.Background(), map[string]bool{
		"exec": true, "cp": true, "run": true, "login": true, "secret": true,
	}); err != nil {
		slog.Error("rootless setup helper failed", "error", err)
		os.Exit(1)
	}
}
