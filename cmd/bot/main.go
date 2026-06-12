package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/runixer/laplaced/internal/agent/imagegen"
	"github.com/runixer/laplaced/internal/app"
	"github.com/runixer/laplaced/internal/bot"
	botTools "github.com/runixer/laplaced/internal/bot/tools"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/secrets"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/web"
)

// imageGenAdapter bridges the imagegen.Agent (domain-specific types) with
// the tool-executor's narrower ImageGenerator interface.
type imageGenAdapter struct{ agent *imagegen.Agent }

func (a *imageGenAdapter) Generate(ctx context.Context, req botTools.ImageGenRequest) (*botTools.ImageGenResponse, error) {
	resp, err := a.agent.Generate(ctx, imagegen.Request{
		UserID:      req.UserID,
		Prompt:      req.Prompt,
		InputImages: req.InputImages,
		AspectRatio: req.AspectRatio,
		ImageSize:   req.ImageSize,
	})
	if err != nil {
		return nil, mapImagegenFailure(err)
	}
	imgs := make([]botTools.ImageGenImage, len(resp.Images))
	for i, img := range resp.Images {
		imgs[i] = botTools.ImageGenImage{MimeType: img.MimeType, Data: img.Data}
	}
	return &botTools.ImageGenResponse{
		Images:      imgs,
		TextContent: resp.TextContent,
	}, nil
}

// mapImagegenFailure translates the agent-side typed error into the tools-side
// mirror so the tool wrapper can errors.As against its own type without
// importing the agent package. Identity for non-typed errors.
func mapImagegenFailure(err error) error {
	var f *imagegen.ImagegenFailure
	if !errors.As(err, &f) {
		return err
	}
	return &botTools.ImageGenFailure{
		Kind:     toToolsImageGenKind(f.Kind),
		Text:     f.Text,
		Provider: f.Provider,
		Cause:    f.Cause,
	}
}

func toToolsImageGenKind(k imagegen.FailureKind) botTools.ImageGenFailureKind {
	switch k {
	case imagegen.KindTimeout:
		return botTools.ImageGenKindTimeout
	case imagegen.KindUpstreamError:
		return botTools.ImageGenKindUpstreamError
	case imagegen.KindTextRefusal:
		return botTools.ImageGenKindTextRefusal
	case imagegen.KindSilentBlockOAI:
		return botTools.ImageGenKindSilentBlockOAI
	case imagegen.KindUnknownNoImages:
		return botTools.ImageGenKindUnknownNoImages
	default:
		return botTools.ImageGenKindUnknown
	}
}

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
	// IMPORTANT: the healthcheck MUST NOT resolve secrets from Vault. Liveness must
	// not depend on Vault — otherwise a Vault outage fails the probe and makes k8s
	// restart an otherwise-healthy container (the bot reads secrets once at startup
	// and never calls Vault again, so a running bot survives a Vault outage). We only
	// Load() config (pure, no network) to learn the port, then probe local /healthz.
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

	// Reject non-numeric port values so nothing user-supplied can reshape the URL.
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Healthcheck: invalid port %q\n", port)
		return 1
	}

	url := fmt.Sprintf("http://localhost:%d/healthz", portNum)
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	// gosec's taint analysis flags any non-literal URL in Get().
	// Here the host is literal "localhost" and the port is a validated uint16 above;
	// no untrusted input remains in the URL.
	resp, err := client.Get(url) // #nosec G704
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

// logTimeFormat truncates time to milliseconds for cleaner logs.
func logTimeFormat(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		if t, ok := a.Value.Any().(time.Time); ok {
			// Format: 2006-01-02T15:04:05.000Z07:00 (milliseconds, not nanoseconds)
			a.Value = slog.StringValue(t.Format("2006-01-02T15:04:05.000Z07:00"))
		}
	}
	return a
}

func main() {
	os.Exit(run())
}

// run is the real entry point; wrapping main ensures deferred cleanup
// (store.Close, etc.) runs before the process exits on any error path.
func run() int {
	// Set up JSON logging early (before config load) with default INFO level.
	// Will be reconfigured with correct level after config is loaded.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: logTimeFormat,
	})))

	// Load .env file if it exists
	if err := app.LoadEnv(); err != nil {
		slog.Warn("failed to load .env", "error", err)
	}

	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	healthcheck := flag.Bool("healthcheck", false, "run healthcheck and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("laplaced", Version)
		return 0
	}

	if *healthcheck {
		return runHealthcheck(*configPath)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		// Can't use logger here, because it's not initialized yet
		slog.Error("failed to load config", "error", err)
		return 1
	}

	// Resolve "vault:" references before validation (which requires the resolved
	// secrets). This startup step is the ONLY path that talks to Vault — secrets are
	// fetched once here, never again, so a later Vault outage cannot affect a running
	// bot (and the healthcheck must never touch Vault). provider stays nil when no
	// [vault] block is configured — a stray reference is then reported by
	// ResolveSecrets, not silently kept. Bound the phase so a wedged Vault cannot
	// hang startup indefinitely.
	secretsCtx, cancelSecrets := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelSecrets()
	var secretProvider config.SecretProvider
	if cfg.Vault != nil {
		p, err := secrets.New(secretsCtx, *cfg.Vault, slog.Default())
		if err != nil {
			slog.Error("failed to initialize vault secret provider", "error", err)
			return 1
		}
		secretProvider = p
	}
	if err := cfg.ResolveSecrets(secretsCtx, secretProvider); err != nil {
		slog.Error("failed to resolve secrets", "error", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		return 1
	}

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		slog.Warn("unknown log level, defaulting to info", "level", cfg.Log.Level)
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       logLevel,
		ReplaceAttr: logTimeFormat,
	}))
	slog.SetDefault(logger)
	logger.Info("Config loaded successfully", "allowed_users", len(cfg.Bot.AllowedUserIDs))

	// Tracer provider lifecycle. Declared here so the shutdown defer runs
	// LAST (LIFO) — after b.Stop / RAGService.Stop / store.Close further
	// below — giving span producers a chance to close their spans before
	// the batcher is drained.
	tracerShutdown, err := obs.InitTracing(context.Background(), obs.TracingConfig{
		Enabled:      cfg.Telemetry.Enabled,
		Exporter:     cfg.Telemetry.Exporter,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
		ServiceName:  cfg.Telemetry.ServiceName,
		TraceContent: cfg.Telemetry.TraceContent,
	}, Version)
	if err != nil {
		logger.Error("failed to initialize tracing", "error", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(shutdownCtx); err != nil {
			logger.Warn("tracer shutdown error", "error", err)
		}
	}()

	if cfg.Bot.Language == "" {
		cfg.Bot.Language = "en"
		logger.Warn("Language not specified in config, defaulting to 'en'")
	}

	store, err := storage.NewStore(storage.Config{
		Driver: cfg.Database.Driver,
		Path:   cfg.Database.Path,
		Postgres: storage.PostgresConfig{
			Host:     cfg.Database.Postgres.Host,
			Port:     cfg.Database.Postgres.Port,
			Database: cfg.Database.Postgres.Database,
			User:     cfg.Database.Postgres.User,
			Password: cfg.Database.Postgres.Password,
			SSLMode:  cfg.Database.Postgres.SSLMode,
		},
	}, logger)
	if err != nil {
		logger.Error("failed to create storage", "error", err)
		return 1
	}
	defer store.Close()

	if err := store.Init(); err != nil {
		logger.Error("failed to initialize storage", "error", err)
		return 1
	}

	// Recovery: Reset zombie artifact states (v0.6.0)
	if err := store.RecoverArtifactStates(cfg.Agents.Extractor.GetRecoveryThreshold()); err != nil {
		logger.Warn("failed to recover artifact states", "error", err)
		// Non-fatal: log and continue
	}

	logger.Info("Database initialized successfully.")

	llmClient, err := llm.NewClient(logger, cfg.LLM.APIKey, cfg.LLM.ProxyURL, cfg.LLM.BaseURL, cfg.LLM.Provider.ToRouting())
	if err != nil {
		logger.Error("failed to create llm client", "error", err)
		return 1
	}
	logger.Info("LLM client created successfully.")

	api, err := telegram.NewExtendedClient(cfg.Telegram.Token, cfg.Telegram.ProxyURL)
	if err != nil {
		logger.Error("failed to create telegram client", "error", err)
		return 1
	}
	logger.Info("Telegram client created successfully.")

	translator, err := i18n.NewTranslator(cfg.Bot.Language)
	if err != nil {
		logger.Error("failed to initialize translator", "error", err)
		return 1
	}
	logger.Info("Translator initialized", "default_lang", cfg.Bot.Language)

	// Initialize all services and agents using shared builder
	services, err := app.SetupServices(context.Background(), logger, cfg, store, llmClient, translator)
	if err != nil {
		logger.Error("failed to setup services", "error", err)
		return 1
	}

	// Create file handler if artifacts are enabled. The blob backend
	// (local disk or S3) was selected by the service builder per config.
	var fileHandler *bot.FileHandler
	fileStorage := services.FileStorage
	if cfg.Artifacts.Enabled {
		fileHandler = bot.NewFileHandler(fileStorage, store, logger)
		logger.Info("Artifacts system enabled")
	} else {
		logger.Info("Artifacts system disabled")
	}

	b, err := bot.NewBot(logger, api, cfg, store, store, store, store, store, store, llmClient, services.RAGService, services.ContextService, translator)
	if err != nil {
		logger.Error("failed to create bot", "error", err)
		return 1
	}
	b.SetAgentLogger(services.AgentLogger)
	b.SetLaplaceAgent(services.LaplaceAgent)
	b.SetReactorAgent(services.ReactorAgent)

	// Set file handler on bot's file processor if enabled
	if fileHandler != nil {
		b.SetFileHandler(fileHandler)
	}

	// Set artifact repository for linking artifacts to messages (v0.6.0)
	b.SetArtifactRepo(store)

	// Wire image generation (v0.8.0) — requires artifacts subsystem. Enabled for
	// every transport (Telegram and Mattermost/Time): the media-reply path is
	// transport-neutral (Bot.transport.SendMedia). Gated only on artifacts + a
	// configured model, so the overlay decides per contour.
	if cfg.Artifacts.Enabled && cfg.Agents.ImageGenerator.Model != "" {
		imgGen := imagegen.New(llmClient, &cfg.Agents.ImageGenerator, logger)
		b.SetImageGenerator(&imageGenAdapter{agent: imgGen})
		b.SetFileStorage(fileStorage)
		logger.Info("Image generation enabled",
			"model", cfg.Agents.ImageGenerator.Model,
			"default_aspect_ratio", cfg.Agents.ImageGenerator.DefaultAspectRatio,
		)
	} else {
		logger.Info("Image generation disabled",
			"artifacts_enabled", cfg.Artifacts.Enabled,
			"model", cfg.Agents.ImageGenerator.Model,
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := services.RAGService.Start(ctx); err != nil {
		logger.Error("failed to start RAG service", "error", err)
		// Don't fail entire bot if RAG fails
	}
	defer services.RAGService.Stop()
	defer b.Stop()

	// Derive webhook path and secret from bot token using SHA-256 BEFORE starting web server
	// to avoid data race (web server reads cfg while main goroutine modifies it).
	//
	// Security notes:
	// - We split the hash: first half for secret header, second half for URL path
	// - Salt is intentionally NOT used: the bot token (~46 chars) is already
	//   a high-entropy cryptographic secret, not a weak password
	// - Brute-forcing SHA-256 of a 46+ char random token is computationally infeasible
	// - Deterministic derivation ensures stable values across restarts
	// - Path hash in logs is acceptable: without the secret header, requests get 403
	if cfg.Telegram.WebhookURL != "" {
		hash := sha256.Sum256([]byte(cfg.Telegram.Token))
		cfg.Telegram.WebhookPath = hex.EncodeToString(hash[16:])   // second half for URL path
		cfg.Telegram.WebhookSecret = hex.EncodeToString(hash[:16]) // first half for secret header
	}

	// Start web server for stats and webhooks
	webServer, err := web.NewServer(ctx, logger, cfg, store, store, store, store, store, store, store, store, b, services.RAGService)
	if err != nil {
		logger.Error("failed to create web server", "error", err)
		return 1
	}
	webServer.SetAgentLogRepo(store)
	webServer.SetPeopleRepository(store)   // v0.5.1: People page
	webServer.SetArtifactRepository(store) // v0.5.2: Artifacts page
	webServer.SetScopeRepository(store)    // label channel scopes in the selector
	if cfg.Artifacts.Enabled {
		webServer.SetFileStorage(fileStorage) // serve artifact downloads from the configured backend
	}
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		if err := webServer.Start(ctx); err != nil {
			logger.Error("web server failed", "error", err)
			cancel() // Trigger graceful shutdown instead of os.Exit
		}
	}()

	logger.Info("Starting Laplaced", "version", Version)

	// ingestionDone is closed when the active transport's ingestion loop stops
	// (Telegram polling goroutine, or Mattermost WS goroutine).
	ingestionDone := make(chan struct{})

	if cfg.Transport == "mattermost" {
		mmClient, err := mattermost.NewClient(ctx, mattermost.Config{
			ServerURL: cfg.Mattermost.ServerURL,
			BotToken:  cfg.Mattermost.BotToken,
			ProxyURL:  cfg.Mattermost.ProxyURL,
		}, logger)
		if err != nil {
			logger.Error("failed to create mattermost client", "error", err)
			return 1
		}
		b.SetScopeRepository(store)
		b.SetTransport(bot.NewMattermostTransport(mmClient, cfg, logger))
		b.SetRenderer(bot.NewMattermostRenderer(mmClient.MaxPostSize(), logger))

		// Principal identity resolution is organic: wired only when the transport
		// config carries a principal_resolver block. Absent → passthrough (DMs get
		// their own isolated scope, today's behavior). Present → federated-passive
		// linkage (trust gate = auth_service, never email).
		if pr := cfg.Mattermost.PrincipalResolver; pr != nil {
			b.SetPrincipalResolver(bot.NewMattermostPrincipalResolver(mmClient, pr.TrustedAuthServices, logger))
			logger.Info("principal resolution enabled", "trusted_auth_services", pr.TrustedAuthServices)
		}
		logger.Info("Mattermost/Time transport active", "server", cfg.Mattermost.ServerURL)

		// WS loop closes mmClient.Events() on ctx cancel; ingestion ranges over it
		// and returns, then we close ingestionDone.
		go mmClient.Run(ctx)
		go func() {
			defer close(ingestionDone)
			b.StartMattermostIngestion(mmClient)
		}()

		<-ctx.Done()
		logger.Info("Shutting down...")
		<-ingestionDone
		logger.Info("Mattermost ingestion stopped")
		<-srvDone
		logger.Info("Web server stopped")
		return 0
	}

	if cfg.Telegram.WebhookURL != "" {
		// WebhookPath and WebhookSecret were already computed above (before web server start)
		// to avoid data race.
		webhookURL := cfg.Telegram.WebhookURL + "/telegram/" + cfg.Telegram.WebhookPath
		if err := b.SetWebhook(webhookURL, cfg.Telegram.WebhookSecret); err != nil {
			logger.Error("failed to set webhook", "error", err)
			return 1
		}
		logger.Info("Webhook set", "url", cfg.Telegram.WebhookURL)
		close(ingestionDone) // No polling, close immediately
	} else {
		logger.Info("Webhook not set, using long polling.")

		// Clear webhook first to ensure we can get updates
		if err := b.SetWebhook("", ""); err != nil {
			logger.Warn("failed to clear webhook", "error", err)
		}

		go func() {
			defer close(ingestionDone)
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
	<-ingestionDone
	logger.Info("Polling stopped")

	// Wait for web server to stop
	<-srvDone
	logger.Info("Web server stopped")
	return 0
}
