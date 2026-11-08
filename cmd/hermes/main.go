// Command hermes is the Go port of the Hermes agent.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/phzietsman/hermes-agent/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], stdio{in: os.Stdin, out: os.Stdout, err: os.Stderr}, app.Options{}))
}
