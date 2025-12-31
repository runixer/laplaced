package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
)

func main() {
	userID := flag.Int64("user_id", 0, "User ID to reset")
	configPath := flag.String("config", "configs/config.yaml", "Path to config file")
	flag.Parse()

	if *userID == 0 {
		fmt.Println("Please provide -user_id")
		os.Exit(1)
	}

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Init storage
	store, err := storage.NewSQLiteStore(logger, cfg.Database.Path)
	if err != nil {
		fmt.Printf("Failed to init storage: %v\n", err)
		os.Exit(1)
	}
	// We don't strictly need Init() (migrations) for deletion, but it's safer to ensure tables exist
	if err := store.Init(); err != nil {
		fmt.Printf("Failed to init DB schema: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	fmt.Printf("Resetting data for user %d...\n", *userID)
	if err := store.ResetUserData(*userID); err != nil {
		fmt.Printf("Failed to reset user data: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Successfully reset user data.")
}
