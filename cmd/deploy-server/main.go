package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"crsf2/internal/deployserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := deployserver.Run(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
