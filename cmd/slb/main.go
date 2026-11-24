package main

import (
	"context"
	"os"

	"github.com/storenth/ecmp-load-balancer/internal/cli"
)

func main() {
	if err := cli.Execute(context.Background(), os.Stdout, os.Stderr); err != nil {
		cli.Logger(os.Stderr).Error("command failed", "error", err)
		os.Exit(1)
	}
}
