package app

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// LoadEnv loads .env file from current working directory.
// Silently ignores if file not found, returns error for other failures.
func LoadEnv() error {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load .env: %w", err)
	}
	return nil
}
