// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Command takecare sets up and keeps a TakeCare install: it works on files
// in a Git working copy and reads the cluster, and never writes into a
// cluster (spec 13).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agaetis-it/takecare-agents/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
