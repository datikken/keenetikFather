package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"crsf2/internal/setupserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка: определить каталог проекта:", err)
		os.Exit(1)
	}
	if err := setupserver.Run(ctx, root, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
