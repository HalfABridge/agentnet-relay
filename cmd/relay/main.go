package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/betta-lab/agentnet-relay/internal/relay"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "agentnet.db", "SQLite database path")
	flag.Parse()

	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatalf("failed to load .env: %v", err)
	}

	roomGCIdleAfterMins := 60
	if value, ok := os.LookupEnv("ROOM_GC_AFTER_IDLE_MINS"); ok && value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			log.Fatalf("invalid ROOM_GC_AFTER_IDLE_MINS %q: %v", value, err)
		}
		roomGCIdleAfterMins = parsed
	}

	roomGCIdleAfter := time.Duration(roomGCIdleAfterMins) * time.Minute

	srv, err := relay.New(*addr, *dbPath, roomGCIdleAfter)
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
