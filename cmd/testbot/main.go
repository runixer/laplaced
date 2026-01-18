// Command testbot provides standalone CLI for autonomous bot testing.
//
// Usage:
//
//	./testbot send "My name is Alice" \
//	  --check-response "Alice" \
//	  --output json
package main

import (
	"os"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		// Cobra already printed the error
		// Exit with non-zero status
		os.Exit(1)
	}
}
