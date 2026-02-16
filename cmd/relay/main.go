package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/betta-lab/agentnet-relay/internal/relay"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "agentnet.db", "SQLite database path")
	flag.Parse()

	srv, err := relay.New(*addr, *dbPath)
	if err != nil {
		log.Fatalf("failed to create relay: %v", err)
	}

	go func() {
		log.Printf("AgentNet relay listening on %s", *addr)
		if err := srv.Start(); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	srv.Shutdown()
}
