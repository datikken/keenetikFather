package main

import (
	"context"
	"os"
	"os/signal"

	"crsf2/internal/addpair"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(addpair.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
