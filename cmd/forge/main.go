// Command forge fetches Helm chart dependencies from OCI registries.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mmarszalek/helm-forge/internal/cli"
)

// Version is set at build time: go build -ldflags "-X main.Version=0.1.0".
var Version = "dev"

func main() {
	cli.Version = Version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
