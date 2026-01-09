package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/bot"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/web"
	"github.com/runixer/laplaced/internal/yandex"

	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var Version = "dev"

var buildInfo = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "laplaced",
		Name:      "build_info",
		Help:      "Build information with version and Go runtime details",
	},
	[]string{"version", "go_version"},
)

func init() {
	buildInfo.WithLabelValues(Version, runtime.Version()).Set(1)
}

func runHealthcheck(configPath string) int {
	// Try to load config to get the port
	// We suppress errors here because if config fails, we might still want to try default port
	// or maybe the app is running with env vars only.
	cfg, err := config.Load(configPath)
	port := "9081"
	if err == nil && cfg.Server.ListenPort != "" {
		port = cfg.Server.ListenPort
	} else {
		// Fallback to env var if config load failed
		if envPort := os.Getenv("LAPLACED_SERVER_PORT"); envPort != "" {
			port = envPort
		}
	}

	url := fmt.Sprintf("http://localhost:%s/healthz", port)
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Healthcheck failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Healthcheck returned status: %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func main() {
	// Set up JSON logging early (before config load) with default INFO level.
	// Will be reconfigured with correct level after config is loaded.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		slog.Debug("No .env file found or failed to load, relying on environment variables")
	}

	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	healthcheck := flag.Bool("healthcheck", false, "run healthcheck and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("laplaced", Version)
		os.Exit(0)
	}

	if *healthcheck {
		os.Exit(runHealthcheck(*configPath))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		// Can't use logger here, because it's not initialized yet
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		slog.Warn("unknown log level, defaulting to info", "level", cfg.Log.Level)
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)
	logger.Info("Config loaded successfully", "allowed_users", len(cfg.Bot.AllowedUserIDs))

	if cfg.Bot.Language == "" {
		cfg.Bot.Language = "en"
		logger.Warn("Language not specified in config, defaulting to 'en'")
	}

	store, err := storage.NewSQLiteStore(logger, cfg.Database.Path)
	if err != nil {
		logger.Error("failed to create storage", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Init(); err != nil {
		logger.Error("failed to initialize storage", "error", err)
		os.Exit(1)
	}
	logger.Info("Database initialized successfully.")

	openrouterClient, err := openrouter.NewClient(logger, cfg.OpenRouter.APIKey, cfg.OpenRouter.ProxyURL)
	if err != nil {
		logger.Error("failed to create openrouter client", "error", err)
		os.Exit(1)
	}
	logger.Info("OpenRouter client created successfully.")

	var speechKitClient yandex.Client
	if cfg.Yandex.Enabled {
		var err error
		speechKitClient, err = yandex.NewSpeechKitClient(context.Background(), logger, cfg.Yandex.APIKey, cfg.Yandex.FolderID, cfg.Yandex.Language, cfg.Yandex.AudioFormat, cfg.Yandex.SampleRate, "stt.api.cloud.yandex.net:443")
		if err != nil {
			logger.Error("failed to create speechkit client", "error", err)
			os.Exit(1)
		}
		logger.Info("Yandex SpeechKit client created successfully.")
	} else {
		logger.Info("Yandex SpeechKit is disabled.")
	}

	api, err := telegram.NewExtendedClient(cfg.Telegram.Token, cfg.Telegram.ProxyURL)
	if err != nil {
		logger.Error("failed to create telegram client", "error", err)
		os.Exit(1)
	}
	logger.Info("Telegram client created successfully.")

	translator, err := i18n.NewTranslator(cfg.Bot.Language)
	if err != nil {
		logger.Error("failed to initialize translator", "error", err)
		os.Exit(1)
	}
	logger.Info("Translator initialized", "default_lang", cfg.Bot.Language)

	// Create agent logger for debugging LLM calls (only logs when debug mode is enabled)
	agentLogger := agentlog.NewLogger(store, logger, cfg.Server.DebugMode)

	// Create context service for shared user context across agents
	contextService := agent.NewContextService(store, store, cfg, logger)

	memoryService := memory.NewService(logger, cfg, store, store, store, openrouterClient, translator)
	memoryService.SetAgentLogger(agentLogger)

	ragService := rag.NewService(logger, cfg, store, store, store, store, store, openrouterClient, memoryService, translator)
	ragService.SetAgentLogger(agentLogger)
	memoryService.SetVectorSearcher(ragService)
	memoryService.SetTopicRepository(store)

	b, err := bot.NewBot(logger, api, cfg, store, store, store, store, store, openrouterClient, speechKitClient, ragService, contextService, translator)
	if err != nil {
		logger.Error("failed to create bot", "error", err)
		os.Exit(1)
	}
	b.SetAgentLogger(agentLogger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := ragService.Start(ctx); err != nil {
		logger.Error("failed to start RAG service", "error", err)
		// Don't fail entire bot if RAG fails
	}
	defer ragService.Stop()
	defer b.Stop()

	// Start web server for stats and webhooks
	webServer, err := web.NewServer(ctx, logger, cfg, store, store, store, store, store, store, store, b, ragService)
	if err != nil {
		logger.Error("failed to create web server", "error", err)
		os.Exit(1)
	}
	webServer.SetAgentLogRepo(store)
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		if err := webServer.Start(ctx); err != nil {
			logger.Error("web server failed", "error", err)
			cancel() // Trigger graceful shutdown instead of os.Exit
		}
	}()

	logger.Info("Starting Laplaced", "version", Version)

	// Channel to wait for polling goroutine (only used in long polling mode)
	pollingDone := make(chan struct{})

	if cfg.Telegram.WebhookURL != "" {
		// Derive webhook path and secret from bot token using SHA-256.
		//
		// Security notes:
		// - We split the hash: first half for secret header, second half for URL path
		// - Salt is intentionally NOT used: the bot token (~46 chars) is already
		//   a high-entropy cryptographic secret, not a weak password
		// - Brute-forcing SHA-256 of a 46+ char random token is computationally infeasible
		// - Deterministic derivation ensures stable values across restarts
		// - Path hash in logs is acceptable: without the secret header, requests get 403
		hash := sha256.Sum256([]byte(cfg.Telegram.Token))
		cfg.Telegram.WebhookPath = hex.EncodeToString(hash[16:])   // second half for URL path
		cfg.Telegram.WebhookSecret = hex.EncodeToString(hash[:16]) // first half for secret header

		webhookURL := cfg.Telegram.WebhookURL + "/telegram/" + cfg.Telegram.WebhookPath
		if err := b.SetWebhook(webhookURL, cfg.Telegram.WebhookSecret); err != nil {
			logger.Error("failed to set webhook", "error", err)
			os.Exit(1)
		}
		logger.Info("Webhook set", "url", cfg.Telegram.WebhookURL)
		close(pollingDone) // No polling, close immediately
	} else {
		logger.Info("Webhook not set, using long polling.")

		// Clear webhook first to ensure we can get updates
		if err := b.SetWebhook("", ""); err != nil {
			logger.Warn("failed to clear webhook", "error", err)
		}

		go func() {
			defer close(pollingDone)
			offset := 0
			for {
				select {
				case <-ctx.Done():
					logger.Info("Polling goroutine received shutdown signal")
					return
				default:
					updates, err := b.API().GetUpdates(ctx, telegram.GetUpdatesRequest{
						Offset:         offset,
						Timeout:        25, // Use 25s to avoid http client timeout (30s)
						AllowedUpdates: []string{"message", "edited_message", "callback_query"},
					})
					if err != nil {
						// Only log and retry if context is not cancelled (shutdown)
						if ctx.Err() == nil {
							logger.Error("failed to get updates", "error", err)
							time.Sleep(5 * time.Second)
						}
						continue
					}

					for i := range updates {
						update := &updates[i]
						if update.UpdateID >= offset {
							offset = update.UpdateID + 1
						}
						// Process in a separate goroutine to not block polling
						b.ProcessUpdateAsync(ctx, update, "long_polling")
					}
				}
			}
		}()
	}

	<-ctx.Done()
	logger.Info("Shutting down...")

	// Wait for polling goroutine to stop
	<-pollingDone
	logger.Info("Polling stopped")

	// Wait for web server to stop
	<-srvDone
	logger.Info("Web server stopped")
}
