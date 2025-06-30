package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.Println("Starting bot...")

	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	bot, err := NewBot(config)
	if err != nil {
		log.Fatalf("Error creating bot: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down bot...")
		cancel()
	}()

	if err := bot.Start(ctx); err != nil {
		log.Printf("Bot stopped: %v", err)
	}
}