package main

import (
	"context"
	"os/signal"
	"syscall"
)

// runKeep calls ready (if set) once handlers are installed, then blocks until SIGTERM/SIGINT; the process exists only to own a registry pid.
func runKeep(ready func()) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if ready != nil {
		ready() // handler installed: safe to signal
	}
	<-ctx.Done()
	return 0
}
