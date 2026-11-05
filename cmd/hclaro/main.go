package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hclaro/internal/cli"
)

var version = "0.1.0-alpha.5"

func main() { os.Exit(run()) }
func run() int {
	o, err := cli.Parse(os.Args[1:], os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hclaro:", err)
		return 2
	}
	if o.Version {
		fmt.Fprintln(os.Stdout, "hclaro", version)
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.Run(ctx, o, os.Stdout, version); err != nil {
		fmt.Fprintln(os.Stderr, "hclaro:", err)
		return 1
	}
	return 0
}
