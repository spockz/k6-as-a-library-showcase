package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k6-as-a-library/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := app.NewRootCommand()
	if err := command.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
