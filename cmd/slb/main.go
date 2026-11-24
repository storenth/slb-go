package main

import (
	"context"
	"os"

	"github.com/storenth/slb-go/internal/cli"
)

func main() {
	_ = cli.Execute(context.Background(), os.Stdout, os.Stderr)
}
