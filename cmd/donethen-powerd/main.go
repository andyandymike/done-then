package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/andyandymike/done-then/internal/powerdaemon"
)

func main() {
	var config powerdaemon.Config
	var fireJobID string
	flag.StringVar(&config.SocketPath, "socket", "/run/donethen/powerd.sock", "root-owned Unix socket")
	flag.StringVar(&config.StateDirectory, "state-dir", "/var/lib/donethen", "root-owned helper state directory")
	flag.StringVar(&config.GroupName, "group", "donethen", "group permitted to connect")
	flag.StringVar(&fireJobID, "fire-job", "", "internal timer callback for one validated job")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "donethen-powerd accepts no positional arguments")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if fireJobID != "" {
		if err := powerdaemon.Fire(ctx, config, fireJobID); err != nil {
			fmt.Fprintf(os.Stderr, "donethen-powerd fire: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := powerdaemon.Run(ctx, config); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "donethen-powerd: %v\n", err)
		os.Exit(1)
	}
}
