package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/runixer/laplaced/internal/app"
	"github.com/runixer/laplaced/internal/bot"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/spf13/cobra"
)

const (
	defaultFallbackUserID = 123
	defaultMessageLimit   = 100
	defaultConfigSubPath  = "configs/config.yaml"
	defaultTestDBPath     = "data/laplaced.db"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey int

const (
	testbotKey contextKey = iota
	optionsKey
)

// testbotOptions holds all CLI flag values, passed via context.
type testbotOptions struct {
	cfgFile   string
	userID    int64
	dbPath    string
	dbChanged bool
	verbose   bool
}

// testBot wraps a full bot instance for CLI testing.
type testBot struct {
	logger    *slog.Logger
	store     *storage.SQLiteStore
	cfg       *config.Config
	bot       *bot.Bot
	storePath string
	tempDir   string
	services  *app.Services
}

// Root command
var rootCmd = &cobra.Command{
	Use:   "testbot",
	Short: "CLI tool for testing laplaced bot",
	Long: `Testbot provides a command-line interface for testing the laplaced Telegram bot
without running the full Telegram integration. It supports sending messages,
checking database state, and processing sessions for fact extraction.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Parse flags into options struct
		opts := &testbotOptions{
			cfgFile:   cmd.Flags().Lookup("config").Value.String(),
			dbPath:    cmd.Flags().Lookup("db").Value.String(),
			dbChanged: cmd.Flags().Lookup("db").Changed,
			verbose:   cmd.Flags().Lookup("verbose").Changed,
		}
		if userIDVal, err := cmd.Flags().GetInt64("user"); err == nil {
			opts.userID = userIDVal
		}

		// Load .env from CWD - fail only if config was explicitly provided
		if err := app.LoadEnv(); err != nil {
			if opts.cfgFile != "" {
				return fmt.Errorf("failed to load .env: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: failed to load .env: %v\n", err)
		}

		// Get default user ID from environment
		defaultUserID := getDefaultUserID()
		if defaultUserID == 0 {
			defaultUserID = defaultFallbackUserID
		}
		if opts.userID == 0 {
			opts.userID = defaultUserID
		}

		// Validate user ID
		if opts.userID <= 0 {
			return fmt.Errorf("invalid user ID: %d (must be positive)", opts.userID)
		}

		// Resolve config path (empty is OK - will use defaults)
		resolvedCfgPath, err := findConfigPath(opts.cfgFile)
		if err != nil && opts.cfgFile != "" {
			// User explicitly provided a config path that doesn't exist
			return fmt.Errorf("failed to find config: %w", err)
		}

		// Load config (uses defaults if path is empty)
		cfg, err := loadConfig(resolvedCfgPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Validate API key from config
		if cfg.OpenRouter.APIKey == "" {
			return fmt.Errorf("LAPLACED_OPENROUTER_API_KEY not set in config/env")
		}

		// Create logger (quiet by default, verbose shows all logs)
		var logger *slog.Logger
		if opts.verbose {
			logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			}))
		} else {
			logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			}))
		}

		tb, err := setupTestBot(cfg, logger, opts.dbPath, opts.dbChanged, resolvedCfgPath)
		if err != nil {
			return fmt.Errorf("failed to setup testbot: %w", err)
		}

		// Store both testbot and options in context for commands to access
		ctx := context.WithValue(cmd.Context(), testbotKey, tb)
		ctx = context.WithValue(ctx, optionsKey, opts)
		cmd.SetContext(ctx)

		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		// Cleanup testbot
		tb := getTestBot(cmd)
		if tb != nil {
			if err := tb.close(); err != nil {
				return fmt.Errorf("failed to close testbot: %w", err)
			}
		}
		return nil
	},
}

// getTestBot retrieves the testBot instance from context.
func getTestBot(cmd *cobra.Command) *testBot {
	if tb := cmd.Context().Value(testbotKey); tb != nil {
		return tb.(*testBot)
	}
	return nil
}

// getOptions retrieves the testbotOptions from context.
func getOptions(cmd *cobra.Command) *testbotOptions {
	if opts := cmd.Context().Value(optionsKey); opts != nil {
		return opts.(*testbotOptions)
	}
	return nil
}

// getUserID retrieves the user ID from options.
func getUserID(cmd *cobra.Command) int64 {
	if opts := getOptions(cmd); opts != nil {
		return opts.userID
	}
	return 0
}

func init() {
	rootCmd.PersistentFlags().String("config", "", "Path to config file (default: auto-detect)")
	rootCmd.PersistentFlags().Int64P("user", "u", 0, "User ID for testing (default: first from LAPLACED_ALLOWED_USER_IDS)")
	rootCmd.PersistentFlags().String("db", "", "Database path (default: data/laplaced.db, use 'data/prod/laplaced.db' for production copy, '--db \"\"' for temp DB)")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Verbose debug output (shows all logs)")
}

// setupTestBot initializes the test bot with all dependencies.
func setupTestBot(cfg *config.Config, logger *slog.Logger, dbPath string, dbChanged bool, resolvedCfgPath string) (*testBot, error) {
	tb := &testBot{
		logger: logger,
	}

	// Track success for cleanup-on-error
	var success bool
	cleanup := func() {
		if tb.store != nil {
			_ = tb.store.Close()
		}
		if tb.tempDir != "" {
			_ = os.RemoveAll(tb.tempDir)
		}
	}
	defer func() {
		if !success {
			cleanup()
		}
	}()

	// Setup database path
	if dbChanged {
		if dbPath == "" {
			// Explicitly empty --db flag: use temp DB
			tb.tempDir = os.TempDir()
			tb.storePath = filepath.Join(tb.tempDir, fmt.Sprintf("laplaced_test_%s.db", getTempFileSuffix()))
		} else {
			// Use provided database path (e.g., prod DB)
			tb.storePath = dbPath
			tb.tempDir = "" // Don't cleanup user-provided DB
		}
	} else {
		// No --db flag: use default test DB
		tb.storePath = defaultTestDBPath

		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(tb.storePath), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create data directory: %w", err)
		}
	}

	// Setup config
	tb.cfg = cfg
	tb.cfg.Database.Path = tb.storePath

	// Log paths in verbose mode
	if logger.Handler().Enabled(context.TODO(), slog.LevelInfo) {
		logger.Info("Using config",
			"path", resolvedCfgPath,
			"source", func() string {
				if resolvedCfgPath != "" {
					return "file"
				}
				return "defaults"
			}())
		logger.Info("Using database",
			"path", tb.storePath,
			"type", func() string {
				if dbPath != "" {
					return "provided"
				}
				return "temp"
			}())
	}

	// Create store
	var err error
	tb.store, err = storage.NewSQLiteStore(tb.logger, tb.storePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}
	if err := tb.store.Init(); err != nil {
		return nil, fmt.Errorf("failed to init store: %w", err)
	}

	// Create translator
	translator, err := i18n.NewTranslator("en")
	if err != nil {
		return nil, fmt.Errorf("failed to create translator: %w", err)
	}

	// Create OpenRouter client
	client, err := openrouter.NewClient(tb.logger, cfg.OpenRouter.APIKey, cfg.OpenRouter.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenRouter client: %w", err)
	}

	// Initialize all services using shared builder
	services, err := app.SetupServices(context.Background(), tb.logger, tb.cfg, tb.store, client, translator)
	if err != nil {
		return nil, fmt.Errorf("failed to setup services: %w", err)
	}
	tb.services = services

	// Use no-op BotAPI (no Telegram integration needed)
	mockAPI := &noOpBotAPI{}

	// Create bot
	tb.bot, err = bot.NewBot(
		tb.logger,
		mockAPI,
		tb.cfg,
		tb.store,
		tb.store,
		tb.store,
		tb.store,
		tb.store,
		tb.store,
		client,
		nil, // No speech kit client
		services.RAGService,
		services.ContextService,
		translator,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}
	tb.bot.SetAgentLogger(services.AgentLogger)
	tb.bot.SetLaplaceAgent(services.LaplaceAgent)

	// Load vectors for RAG, but don't start background loops
	// Background loops interfere with explicit ForceProcessUser calls
	if err := services.RAGService.ReloadVectors(); err != nil {
		return nil, fmt.Errorf("failed to load RAG vectors: %w", err)
	}

	success = true
	return tb, nil
}

// close cleans up resources, collecting any errors.
func (tb *testBot) close() error {
	var errs []error

	// Stop RAG service if running
	if tb.services != nil && tb.services.RAGService != nil {
		tb.services.RAGService.Stop()
	}

	if tb.store != nil {
		if err := tb.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("store.Close: %w", err))
		}
	}

	// Temp database files in /tmp are managed by the OS and cleaned up automatically.
	// No manual cleanup needed for temp DBs created by testbot.

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %w", errors.Join(errs...))
	}
	return nil
}

// getTempFileSuffix returns a cryptographically secure random suffix for temp file naming.
// Panics if crypto/rand fails, as this indicates a critical system failure.
func getTempFileSuffix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// getDefaultUserID returns the first user ID from LAPLACED_ALLOWED_USER_IDS env var.
func getDefaultUserID() int64 {
	allowedUsersStr := os.Getenv("LAPLACED_ALLOWED_USER_IDS")
	if allowedUsersStr == "" {
		return 0
	}

	// Parse comma-separated list
	parts := strings.Split(allowedUsersStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Try to parse as int64
		var id int64
		_, err := fmt.Sscanf(part, "%d", &id)
		if err == nil && id > 0 {
			return id
		}
	}

	return 0
}

// loadConfig loads the configuration from file.
func loadConfig(path string) (*config.Config, error) {
	return config.Load(path)
}

// findConfigPath resolves the config file path.
// Searches in order: provided path, CWD/configs/config.yaml, then defaults.
func findConfigPath(providedPath string) (string, error) {
	// If path provided, verify it exists
	if providedPath != "" {
		if _, err := os.Stat(providedPath); err == nil {
			return providedPath, nil
		}
		// User explicitly provided a path that doesn't exist
		return "", fmt.Errorf("config file not found: %s", providedPath)
	}

	// Try CWD/configs/config.yaml
	cwdConfig := defaultConfigSubPath
	if _, err := os.Stat(cwdConfig); err == nil {
		return cwdConfig, nil
	}

	// Config not found - return empty string (will use defaults)
	return "", nil
}

// Helper functions for command output

func outputJSON(result *rag.TestMessageResult, failures []string) error {
	// Process response through markdown pipeline (same as real bot)
	htmlResponse, err := markdown.ToHTML(result.Response)
	if err != nil {
		htmlResponse = result.Response // Fallback to raw on error
	}

	output := map[string]interface{}{
		"status": "PASS",
	}

	if len(failures) > 0 {
		output["status"] = "FAIL"
		output["failures"] = failures
	}

	output["response_raw"] = result.Response
	output["response_html"] = htmlResponse
	output["duration_ms"] = result.TimingTotal.Milliseconds()
	output["metrics"] = map[string]interface{}{
		"tokens":         result.PromptTokens + result.CompletionTokens,
		"cost_usd":       result.TotalCost,
		"topics_matched": result.TopicsMatched,
		"facts_injected": result.FactsInjected,
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func outputText(result *rag.TestMessageResult, failures []string) error {
	status := "PASS"
	if len(failures) > 0 {
		status = "FAIL"
	}

	// Process response through markdown pipeline (same as real bot)
	htmlResponse, err := markdown.ToHTML(result.Response)
	if err != nil {
		htmlResponse = result.Response // Fallback to raw on error
	}

	fmt.Printf("Status: %s\n", status)
	fmt.Printf("Response (processed):\n%s\n", htmlResponse)
	fmt.Printf("Duration: %v\n", result.TimingTotal)
	fmt.Printf("Tokens: %d\n", result.PromptTokens+result.CompletionTokens)
	fmt.Printf("Cost: $%.6f\n", result.TotalCost)
	fmt.Printf("Facts injected: %d\n", result.FactsInjected)

	if len(failures) > 0 {
		fmt.Printf("\nFailures:\n")
		for _, f := range failures {
			fmt.Printf("  - %s\n", f)
		}
	}

	return nil
}

// outputCheckJSON outputs check results in JSON format.
func outputCheckJSON(data map[string]interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// Flag retrieval helpers that panic on error (error indicates bug in flag name).
// These are preferred over cmd.Flags().GetXxx() to make intent explicit and avoid
// ignored error linting warnings.

// mustGetString retrieves a string flag value. Panics on error (indicates bug in flag name).
func mustGetString(cmd *cobra.Command, name string) string {
	val, err := cmd.Flags().GetString(name)
	if err != nil {
		panic(fmt.Sprintf("bug: failed to get flag %q: %v", name, err))
	}
	return val
}

// mustGetInt retrieves an int flag value. Panics on error (indicates bug in flag name).
func mustGetInt(cmd *cobra.Command, name string) int {
	val, err := cmd.Flags().GetInt(name)
	if err != nil {
		panic(fmt.Sprintf("bug: failed to get flag %q: %v", name, err))
	}
	return val
}

// mustGetBool retrieves a bool flag value. Panics on error (indicates bug in flag name).
func mustGetBool(cmd *cobra.Command, name string) bool {
	val, err := cmd.Flags().GetBool(name)
	if err != nil {
		panic(fmt.Sprintf("bug: failed to get flag %q: %v", name, err))
	}
	return val
}
