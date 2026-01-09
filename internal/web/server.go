package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/bot"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/ui"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const metricsNamespace = "laplaced"

var (
	memoryFactsCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "facts_count",
			Help:      "Total number of facts in memory per user and type",
		},
		[]string{"user_id", "type"},
	)
	memoryStaleness = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "memory",
			Name:      "staleness_days",
			Help:      "Average age of facts in days per user",
		},
		[]string{"user_id"},
	)
	userInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Name:      "user_info",
			Help:      "User information for label joins (value always 1)",
		},
		[]string{"user_id", "username", "first_name"},
	)
)

func init() {
	prometheus.MustRegister(memoryFactsCount)
	prometheus.MustRegister(memoryStaleness)
	prometheus.MustRegister(userInfo)
}

// getClientIP extracts the real client IP from the request.
// It checks X-Forwarded-For and X-Real-IP headers (set by reverse proxies like traefik),
// falling back to RemoteAddr if no proxy headers are present.
func getClientIP(r *http.Request) string {
	// X-Forwarded-For may contain multiple IPs: "client, proxy1, proxy2"
	// The first one is the original client IP
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// X-Real-IP is typically set by nginx/traefik
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return xrip
	}

	// Fallback to RemoteAddr (strips port if present)
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// BotInterface is an interface that abstracts the bot's functionality needed by the web server.
type BotInterface interface {
	HandleUpdateAsync(ctx context.Context, update json.RawMessage, remoteAddr string)
	API() telegram.BotAPI
	GetActiveSessions() ([]rag.ActiveSessionInfo, error)
	ForceCloseSession(ctx context.Context, userID int64) (int, error)
	ForceCloseSessionWithProgress(ctx context.Context, userID int64, onProgress rag.ProgressCallback) (*rag.ProcessingStats, error)
	SendTestMessage(ctx context.Context, userID int64, text string, saveToHistory bool) (*rag.TestMessageResult, error)
}

// dashboardStatsCache holds cached dashboard stats with TTL
type dashboardStatsCache struct {
	mu      sync.RWMutex
	entries map[int64]*dashboardStatsCacheEntry
	ttl     time.Duration
}

type dashboardStatsCacheEntry struct {
	stats   *storage.DashboardStats
	expires time.Time
}

func newDashboardStatsCache(ttl time.Duration) *dashboardStatsCache {
	return &dashboardStatsCache{
		entries: make(map[int64]*dashboardStatsCacheEntry),
		ttl:     ttl,
	}
}

func (c *dashboardStatsCache) Get(userID int64) (*storage.DashboardStats, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[userID]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.stats, true
}

func (c *dashboardStatsCache) Set(userID int64, stats *storage.DashboardStats) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[userID] = &dashboardStatsCacheEntry{
		stats:   stats,
		expires: time.Now().Add(c.ttl),
	}
}

type Server struct {
	cfg             *config.Config
	factRepo        storage.FactRepository
	userRepo        storage.UserRepository
	statsRepo       storage.StatsRepository
	topicRepo       storage.TopicRepository
	msgRepo         storage.MessageRepository
	factHistoryRepo storage.FactHistoryRepository
	maintenanceRepo storage.MaintenanceRepository
	agentLogRepo    storage.AgentLogRepository
	bot             BotInterface
	rag             *rag.Service
	logger          *slog.Logger
	renderer        *ui.Renderer
	ctx             context.Context // Server's parent context for webhook processing
	statsCache      *dashboardStatsCache
	wg              sync.WaitGroup
}

func NewServer(ctx context.Context, logger *slog.Logger, cfg *config.Config, factRepo storage.FactRepository, userRepo storage.UserRepository, statsRepo storage.StatsRepository, topicRepo storage.TopicRepository, msgRepo storage.MessageRepository, factHistoryRepo storage.FactHistoryRepository, maintenanceRepo storage.MaintenanceRepository, bot BotInterface, rag *rag.Service) (*Server, error) {
	renderer, err := ui.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize renderer: %w", err)
	}

	return &Server{
		cfg:             cfg,
		factRepo:        factRepo,
		userRepo:        userRepo,
		statsRepo:       statsRepo,
		topicRepo:       topicRepo,
		msgRepo:         msgRepo,
		factHistoryRepo: factHistoryRepo,
		maintenanceRepo: maintenanceRepo,
		bot:             bot,
		rag:             rag,
		logger:          logger.With("component", "web_server"),
		renderer:        renderer,
		statsCache:      newDashboardStatsCache(5 * time.Minute),
		ctx:             ctx,
	}, nil
}

// SetAgentLogRepo sets the agent log repository (used for debug UI)
func (s *Server) SetAgentLogRepo(repo storage.AgentLogRepository) {
	s.agentLogRepo = repo
}

func (s *Server) Start(ctx context.Context) error {
	// Check and generate password if needed
	if s.cfg.Server.Auth.Enabled && s.cfg.Server.Auth.Password == "" {
		bytes := make([]byte, 6) // 12 hex chars
		if _, err := rand.Read(bytes); err != nil {
			return fmt.Errorf("failed to generate random password: %w", err)
		}
		s.cfg.Server.Auth.Password = hex.EncodeToString(bytes)
		fmt.Printf("\n⚠️  Web UI password not set, generated: %s\n\n", s.cfg.Server.Auth.Password)
		s.logger.Info("Web UI password auto-generated (see console output)")
	}

	mux := http.NewServeMux()

	// Register debug routes only if enabled
	if s.cfg.Server.DebugMode {
		mux.HandleFunc("/ui/stats", instrumentHandler("stats", s.statsHandler))
		mux.HandleFunc("/ui/debug/sessions", instrumentHandler("debug_sessions", s.sessionsHandler))
		mux.HandleFunc("/ui/debug/sessions/process", instrumentHandler("debug_sessions_process", s.sessionsProcessSSEHandler))
		mux.HandleFunc("/ui/debug/chat", instrumentHandler("debug_chat", s.debugChatHandler))
		mux.HandleFunc("/ui/debug/chat/send", instrumentHandler("debug_chat_send", s.debugChatSendHandler))
		mux.HandleFunc("/ui/debug/split", instrumentHandler("debug_split", s.splitTopicsHandler))
		mux.HandleFunc("/ui/debug/database", instrumentHandler("debug_database", s.databaseMaintenanceHandler))
		mux.HandleFunc("/ui/debug/database/health", instrumentHandler("debug_database_health", s.databaseHealthHandler))
		mux.HandleFunc("/ui/debug/database/repair", instrumentHandler("debug_database_repair", s.databaseRepairHandler))
		mux.HandleFunc("/ui/debug/database/contamination", instrumentHandler("debug_database_contamination", s.databaseContaminationHandler))
		mux.HandleFunc("/ui/debug/database/contamination/fix", instrumentHandler("debug_database_contamination_fix", s.databaseContaminationFixHandler))
		mux.HandleFunc("/ui/debug/database/purge", instrumentHandler("debug_database_purge", s.databasePurgeHandler))

		// Agent log pages
		mux.HandleFunc("/ui/agents/laplace", instrumentHandler("agent_laplace", s.agentLogHandler("laplace", "Laplace", "robot", "Main chat bot - processes user messages and generates responses")))
		mux.HandleFunc("/ui/agents/reranker", instrumentHandler("agent_reranker", s.agentLogHandler("reranker", "Reranker", "funnel", "Filters and ranks retrieved topics by relevance")))
		mux.HandleFunc("/ui/agents/archivist", instrumentHandler("agent_archivist", s.agentLogHandler("archivist", "Archivist", "archive", "Extracts facts from conversations for long-term memory")))
		mux.HandleFunc("/ui/agents/deduplicator", instrumentHandler("agent_deduplicator", s.agentLogHandler("deduplicator", "Deduplicator", "layers", "Resolves conflicting facts and decides what to keep")))
		mux.HandleFunc("/ui/agents/splitter", instrumentHandler("agent_splitter", s.agentLogHandler("splitter", "Splitter", "scissors", "Splits conversations into topics")))
		mux.HandleFunc("/ui/agents/merger", instrumentHandler("agent_merger", s.agentLogHandler("merger", "Merger", "intersect", "Verifies if topics should be merged")))
		mux.HandleFunc("/ui/agents/enricher", instrumentHandler("agent_enricher", s.agentLogHandler("enricher", "Enricher", "magic", "Expands user queries for better retrieval")))
		mux.HandleFunc("/ui/agents/scout", instrumentHandler("agent_scout", s.agentLogHandler("scout", "Scout", "search", "Web search tool execution logs")))

		// Redirect /ui/ to /ui/stats
		mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ui/" {
				http.Redirect(w, r, "/ui/stats", http.StatusFound)
			} else {
				http.NotFound(w, r)
			}
		})
		s.logger.Info("Debug endpoints enabled at /ui/")
	}

	// Always register healthz and webhook
	mux.HandleFunc("/healthz", instrumentHandler("healthz", s.healthzHandler))
	if s.cfg.Telegram.WebhookPath != "" {
		mux.HandleFunc("/telegram/"+s.cfg.Telegram.WebhookPath, instrumentHandler("webhook", s.webhookHandler))
	}
	mux.Handle("/metrics", promhttp.Handler())

	// Serve static files (favicon, logo, etc.)
	mux.HandleFunc("/favicon.ico", s.faviconHandler)
	mux.HandleFunc("/static/logo.svg", s.logoHandler)

	// Wrap the mux with middlewares
	// Chain: Logging -> Auth -> Mux
	handler := s.basicAuthMiddleware(mux)
	handler = s.loggingMiddleware(handler)

	server := &http.Server{
		Addr:              ":" + s.cfg.Server.ListenPort,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("web server shutdown failed", "error", err)
		}
	}()

	// Update metrics immediately on startup, then periodically
	s.updateMetrics()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.updateMetrics()
			}
		}
	}()

	s.logger.Info("Starting web server", "port", s.cfg.Server.ListenPort)
	err := server.ListenAndServe()
	if err != http.ErrServerClosed {
		return err
	}
	s.wg.Wait() // Wait for background goroutines to finish
	return nil
}

func (s *Server) updateMetrics() {
	if s.factRepo == nil || s.userRepo == nil {
		return
	}

	// Update active sessions count (unprocessed messages waiting for archival)
	if s.bot != nil {
		sessions, err := s.bot.GetActiveSessions()
		if err != nil {
			s.logger.Error("failed to get active sessions for metrics", "error", err)
		} else {
			bot.SetActiveSessions(len(sessions))
		}
	}

	// Get all users for per-user metrics
	users, err := s.userRepo.GetAllUsers()
	if err != nil {
		s.logger.Error("failed to get users for metrics", "error", err)
		return
	}

	// Update user info metric (for label joins in Grafana)
	for _, user := range users {
		userIDStr := fmt.Sprintf("%d", user.ID)
		userInfo.WithLabelValues(userIDStr, user.Username, user.FirstName).Set(1)
	}

	// Update facts count and staleness per user
	for _, user := range users {
		userIDStr := fmt.Sprintf("%d", user.ID)
		stats, err := s.factRepo.GetFactStatsByUser(user.ID)
		if err != nil {
			s.logger.Error("failed to get facts stats for user", "user_id", user.ID, "error", err)
			continue
		}
		for t, count := range stats.CountByType {
			memoryFactsCount.WithLabelValues(userIDStr, t).Set(float64(count))
		}
		memoryStaleness.WithLabelValues(userIDStr).Set(stats.AvgAgeDays)
	}

	// Update topics count per user
	if s.topicRepo != nil {
		for _, user := range users {
			filter := storage.TopicFilter{UserID: user.ID}
			topicResult, err := s.topicRepo.GetTopicsExtended(filter, 1, 0, "", "")
			if err != nil {
				s.logger.Error("failed to get topics count for user", "user_id", user.ID, "error", err)
				continue
			}
			memory.SetTopicsTotal(user.ID, topicResult.TotalCount)
		}
	}

	// Update storage metrics and run cleanup
	if s.maintenanceRepo != nil {
		s.updateStorageMetrics()
		s.runCleanup()
	}
}

// updateStorageMetrics updates database size metrics.
func (s *Server) updateStorageMetrics() {
	// Total DB size
	dbSize, err := s.maintenanceRepo.GetDBSize()
	if err != nil {
		s.logger.Error("failed to get DB size", "error", err)
	} else {
		storage.SetStorageSize(dbSize)
	}

	// Per-table sizes
	tableSizes, err := s.maintenanceRepo.GetTableSizes()
	if err != nil {
		s.logger.Error("failed to get table sizes", "error", err)
	} else {
		for _, ts := range tableSizes {
			storage.SetTableSize(ts.Name, ts.Bytes)
		}
	}
}

// runCleanup performs periodic cleanup of old records.
func (s *Server) runCleanup() {
	// Cleanup fact_history: keep 100 per user
	start := time.Now()
	deleted, err := s.maintenanceRepo.CleanupFactHistory(100)
	duration := time.Since(start).Seconds()
	if err != nil {
		s.logger.Error("failed to cleanup fact_history", "error", err)
	} else {
		storage.RecordCleanupDuration("fact_history", duration)
		if deleted > 0 {
			storage.RecordCleanupDeleted("fact_history", deleted)
			s.logger.Info("cleaned up fact_history", "deleted", deleted, "duration_s", duration)
		}
	}

	// Cleanup agent_logs: keep 50 per user per agent
	start = time.Now()
	deleted, err = s.maintenanceRepo.CleanupAgentLogs(50)
	duration = time.Since(start).Seconds()
	if err != nil {
		s.logger.Error("failed to cleanup agent_logs", "error", err)
	} else {
		storage.RecordCleanupDuration("agent_logs", duration)
		if deleted > 0 {
			storage.RecordCleanupDeleted("agent_logs", deleted)
			s.logger.Info("cleaned up agent_logs", "deleted", deleted, "duration_s", duration)
		}
	}
}

func (s *Server) webhookHandler(w http.ResponseWriter, r *http.Request) {
	// Verify Telegram secret token if configured
	if s.cfg.Telegram.WebhookSecret != "" {
		token := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if token != s.cfg.Telegram.WebhookSecret {
			s.logger.Warn("Webhook request with invalid secret token", "client_ip", getClientIP(r), "user_agent", r.UserAgent())
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	// Limit request body to 10MB to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("failed to read request body", "error", err)
		if strings.Contains(err.Error(), "request body too large") {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Acknowledge the update immediately to prevent Telegram from resending it.
	w.WriteHeader(http.StatusOK)

	// Process the update asynchronously with proper shutdown tracking.
	// Use server's context (not request context) so processing continues after handler returns.
	// HandleUpdateAsync uses the bot's WaitGroup to ensure graceful shutdown.
	s.bot.HandleUpdateAsync(s.ctx, json.RawMessage(body), getClientIP(r))
}

func (s *Server) getCommonData(r *http.Request) (ui.PageData, error) {
	users, err := s.userRepo.GetAllUsers()
	if err != nil {
		return ui.PageData{}, err
	}

	var selectedUserID int64
	if idStr := r.URL.Query().Get("user_id"); idStr != "" {
		_, _ = fmt.Sscanf(idStr, "%d", &selectedUserID)
	}

	return ui.PageData{
		Users:          users,
		SelectedUserID: selectedUserID,
	}, nil
}

func (s *Server) statsHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	statsMap, err := s.statsRepo.GetStats()
	if err != nil {
		s.logger.Error("failed to get stats", "error", err)
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		return
	}

	// Try cache first
	dashboardStats, ok := s.statsCache.Get(pageData.SelectedUserID)
	if !ok {
		var err error
		dashboardStats, err = s.statsRepo.GetDashboardStats(pageData.SelectedUserID)
		if err != nil {
			s.logger.Error("failed to get dashboard stats", "error", err)
			http.Error(w, "Failed to get dashboard stats", http.StatusInternalServerError)
			return
		}
		s.statsCache.Set(pageData.SelectedUserID, dashboardStats)
	}

	// Convert map to slice for stable sorting
	statsSlice := make([]storage.Stat, 0, len(statsMap))
	for _, stat := range statsMap {
		if pageData.SelectedUserID != 0 && stat.UserID != pageData.SelectedUserID {
			continue
		}
		statsSlice = append(statsSlice, stat)
	}

	// Sort by UserID to ensure a consistent order
	sort.Slice(statsSlice, func(i, j int) bool {
		return statsSlice[i].UserID < statsSlice[j].UserID
	})

	// Calculate total stats
	var totalTokens int
	var totalCost float64
	for _, stat := range statsSlice {
		totalTokens += stat.TokensUsed
		totalCost += stat.CostUSD
	}

	data := struct {
		Stats       []storage.Stat
		TotalTokens int
		TotalCost   float64
		Dashboard   *storage.DashboardStats
	}{
		Stats:       statsSlice,
		TotalTokens: totalTokens,
		TotalCost:   totalCost,
		Dashboard:   dashboardStats,
	}

	pageData.Data = data

	if err := s.renderer.Render(w, "stats.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *Server) sessionsHandler(w http.ResponseWriter, r *http.Request) {
	// Handle POST request to force close a session
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		userIDStr := r.FormValue("user_id")
		var userID int64
		if _, err := fmt.Sscanf(userIDStr, "%d", &userID); err != nil {
			http.Error(w, "Invalid user ID", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		processedCount, err := s.bot.ForceCloseSession(ctx, userID)
		if err != nil {
			s.logger.Error("Failed to force close session", "user_id", userID, "error", err)
		} else {
			s.logger.Info("Force closed session via web UI", "user_id", userID, "processed_messages", processedCount)
		}

		// Redirect back to the sessions page
		http.Redirect(w, r, "/ui/debug/sessions", http.StatusSeeOther)
		return
	}

	// GET request - display sessions
	sessions, err := s.bot.GetActiveSessions()
	if err != nil {
		s.logger.Error("failed to get active sessions", "error", err)
		http.Error(w, "Failed to get sessions", http.StatusInternalServerError)
		return
	}

	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Build user lookup map from pageData.Users
	userMap := make(map[int64]storage.User)
	for _, u := range pageData.Users {
		userMap[u.ID] = u
	}

	// Get chunk interval for countdown calculation
	chunkInterval := s.cfg.RAG.GetChunkDuration()

	type sessionDisplayData struct {
		UserID          int64
		UserName        string
		MessageCount    int
		FirstMessageISO string // ISO 8601 for JS parsing
		LastMessageISO  string
		ProcessAtISO    string // When session will be auto-processed
		ContextSize     int    // Characters in session
	}

	displaySessions := make([]sessionDisplayData, 0, len(sessions))
	for _, sess := range sessions {
		userName := fmt.Sprintf("User %d", sess.UserID)
		if u, ok := userMap[sess.UserID]; ok {
			if u.FirstName != "" {
				userName = u.FirstName
				if u.LastName != "" {
					userName += " " + u.LastName
				}
			} else if u.Username != "" {
				userName = "@" + u.Username
			}
		}

		processAt := sess.LastMessageTime.Add(chunkInterval)

		displaySessions = append(displaySessions, sessionDisplayData{
			UserID:          sess.UserID,
			UserName:        userName,
			MessageCount:    sess.MessageCount,
			FirstMessageISO: sess.FirstMessageTime.UTC().Format(time.RFC3339),
			LastMessageISO:  sess.LastMessageTime.UTC().Format(time.RFC3339),
			ProcessAtISO:    processAt.UTC().Format(time.RFC3339),
			ContextSize:     sess.ContextSize,
		})
	}

	pageData.Data = displaySessions

	if err := s.renderer.Render(w, "sessions.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// sessionsProcessSSEHandler handles SSE streaming of session processing progress.
func (s *Server) sessionsProcessSSEHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userIDStr := r.URL.Query().Get("user_id")
	var userID int64
	if _, err := fmt.Sscanf(userIDStr, "%d", &userID); err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	// Create progress callback that sends SSE events
	onProgress := func(event rag.ProgressEvent) {
		data, err := json.Marshal(event)
		if err != nil {
			s.logger.Error("failed to marshal progress event", "error", err)
			return
		}

		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Process with progress
	stats, err := s.bot.ForceCloseSessionWithProgress(ctx, userID, onProgress)
	if err != nil {
		s.logger.Error("Failed to process session", "user_id", userID, "error", err)
		// Send error event
		errorEvent := rag.ProgressEvent{
			Stage:    "error",
			Complete: true,
			Message:  fmt.Sprintf("Error: %s", err.Error()),
		}
		if stats != nil {
			errorEvent.Stats = stats
		}
		data, _ := json.Marshal(errorEvent)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) faviconHandler(w http.ResponseWriter, r *http.Request) {
	data, err := ui.StaticFS.ReadFile("static/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=604800") // 7 days
	if _, err := w.Write(data); err != nil {
		s.logger.Debug("failed to write favicon response", "error", err)
	}
}

func (s *Server) logoHandler(w http.ResponseWriter, r *http.Request) {
	data, err := ui.StaticFS.ReadFile("static/logo.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=604800") // 7 days
	if _, err := w.Write(data); err != nil {
		s.logger.Debug("failed to write logo response", "error", err)
	}
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// We don't log webhook requests at all
		if path == "/telegram/"+s.bot.API().GetToken() {
			next.ServeHTTP(w, r)
			return
		}

		// Log healthz and metrics at debug level, other requests at info level
		if path == "/healthz" || path == "/metrics" {
			s.logger.Debug("Received HTTP request",
				"method", r.Method,
				"path", path,
				"client_ip", getClientIP(r),
			)
		} else {
			s.logger.Info("Received HTTP request",
				"method", r.Method,
				"path", path,
				"client_ip", getClientIP(r),
				"user_agent", r.UserAgent(),
			)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only protect /ui/ routes
		if strings.HasPrefix(r.URL.Path, "/ui/") {
			if !s.cfg.Server.Auth.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok || user != s.cfg.Server.Auth.Username || pass != s.cfg.Server.Auth.Password {
				w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// debugChatHandler handles GET /ui/debug/chat - renders the chat page with message history.
func (s *Server) debugChatHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get recent messages for selected user
	var messages []storage.Message
	if pageData.SelectedUserID > 0 {
		messages, err = s.msgRepo.GetUnprocessedMessages(pageData.SelectedUserID)
		if err != nil {
			s.logger.Error("failed to get messages", "error", err)
		}
	}

	data := struct {
		ui.PageData
		Messages []storage.Message
	}{
		PageData: pageData,
		Messages: messages,
	}

	_ = s.renderer.Render(w, "debug_chat.html", data, ui.GetFuncMap())
}

// debugChatSendRequest is the request body for POST /ui/debug/chat/send.
type debugChatSendRequest struct {
	UserID        int64  `json:"user_id"`
	Message       string `json:"message"`
	SaveToHistory bool   `json:"save_to_history"`
}

// debugChatSendResponse is the response body for POST /ui/debug/chat/send.
type debugChatSendResponse struct {
	Response string `json:"response"`
	Timing   struct {
		TotalMs     int64 `json:"total_ms"`
		EmbeddingMs int64 `json:"embedding_ms"`
		SearchMs    int64 `json:"search_ms"`
		LLMMs       int64 `json:"llm_ms"`
	} `json:"timing"`
	Tokens struct {
		Prompt     int `json:"prompt"`
		Completion int `json:"completion"`
	} `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Context struct {
		TopicsMatched int    `json:"topics_matched"`
		FactsInjected int    `json:"facts_injected"`
		Preview       string `json:"preview"`
	} `json:"context"`
	Error string `json:"error,omitempty"`
}

// splitTopicsHandler handles POST /ui/debug/split - triggers splitting of large topics.
func (s *Server) splitTopicsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		UserID         int64 `json:"user_id"`
		ThresholdChars int   `json:"threshold_chars"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON: " + err.Error()})
		return
	}

	// user_id=0 means all users

	if req.ThresholdChars == 0 {
		req.ThresholdChars = s.cfg.RAG.GetSplitThreshold()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	stats, err := s.rag.SplitLargeTopics(ctx, req.UserID, req.ThresholdChars)
	if err != nil {
		s.logger.Error("SplitLargeTopics failed", "error", err, "user_id", req.UserID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
			"stats": stats,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"stats":   stats,
	})
}

// databaseMaintenanceHandler handles GET /ui/debug/database - renders database maintenance page.
func (s *Server) databaseMaintenanceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	pageData, err := s.getCommonData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := struct {
		ui.PageData
		DefaultThreshold int
	}{
		PageData:         pageData,
		DefaultThreshold: s.cfg.RAG.GetSplitThreshold(),
	}

	_ = s.renderer.Render(w, "database_maintenance.html", data, ui.GetFuncMap())
}

// databaseHealthHandler handles GET /ui/debug/database/health - returns database health info.
func (s *Server) databaseHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var userID int64
	if idStr := r.URL.Query().Get("user_id"); idStr != "" {
		_, _ = fmt.Sscanf(idStr, "%d", &userID)
	}
	// userID=0 means all users

	thresholdChars := 25000
	if t := r.URL.Query().Get("threshold"); t != "" {
		if _, err := fmt.Sscanf(t, "%d", &thresholdChars); err != nil {
			thresholdChars = 25000
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	health, err := s.rag.GetDatabaseHealth(ctx, userID, thresholdChars)
	if err != nil {
		s.logger.Error("GetDatabaseHealth failed", "error", err, "user_id", userID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(health)
}

// databaseRepairHandler handles POST /ui/debug/database/repair - repairs database issues.
func (s *Server) databaseRepairHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		UserID int64 `json:"user_id"`
		DryRun bool  `json:"dry_run"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON: " + err.Error()})
		return
	}
	// user_id=0 means all users

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	stats, err := s.rag.RepairDatabase(ctx, req.UserID, req.DryRun)
	if err != nil {
		s.logger.Error("RepairDatabase failed", "error", err, "user_id", req.UserID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   err.Error(),
			"stats":   stats,
			"dry_run": req.DryRun,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"stats":   stats,
		"dry_run": req.DryRun,
	})
}

// databaseContaminationHandler handles GET /ui/debug/database/contamination - returns cross-user contamination info.
func (s *Server) databaseContaminationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var userID int64
	if idStr := r.URL.Query().Get("user_id"); idStr != "" {
		_, _ = fmt.Sscanf(idStr, "%d", &userID)
	}
	// userID=0 means all users

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	info, err := s.rag.GetContaminationInfo(ctx, userID)
	if err != nil {
		s.logger.Error("GetContaminationInfo failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Log contamination count for debugging
	s.logger.Info("Contamination check result", "count", info.TotalContaminated, "user_id", userID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// databaseContaminationFixHandler handles POST /ui/debug/database/contamination/fix - fixes cross-user contamination.
func (s *Server) databaseContaminationFixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		UserID int64 `json:"user_id"`
		DryRun bool  `json:"dry_run"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON: " + err.Error()})
		return
	}
	// user_id=0 means all users

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	stats, err := s.rag.FixContamination(ctx, req.UserID, req.DryRun)
	if err != nil {
		s.logger.Error("FixContamination failed", "error", err, "user_id", req.UserID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   err.Error(),
			"stats":   stats,
			"dry_run": req.DryRun,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"stats":   stats,
		"dry_run": req.DryRun,
	})
}

// databasePurgeHandler handles POST /ui/debug/database/purge - deletes all debug data.
func (s *Server) databasePurgeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DryRun bool `json:"dry_run"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON: " + err.Error()})
		return
	}

	if s.maintenanceRepo == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Maintenance repository not available"})
		return
	}

	stats := map[string]int64{
		"agent_logs_deleted":   0,
		"fact_history_deleted": 0,
	}

	if req.DryRun {
		// For dry run, count records that would be deleted
		agentLogsCount, err := s.maintenanceRepo.CountAgentLogs()
		if err != nil {
			s.logger.Error("Failed to count agent_logs", "error", err)
		} else {
			stats["agent_logs_deleted"] = agentLogsCount
		}

		factHistoryCount, err := s.maintenanceRepo.CountFactHistory()
		if err != nil {
			s.logger.Error("Failed to count fact_history", "error", err)
		} else {
			stats["fact_history_deleted"] = factHistoryCount
		}
	} else {
		// Actually delete all debug data
		deleted, err := s.maintenanceRepo.CleanupAgentLogs(0)
		if err != nil {
			s.logger.Error("Failed to purge agent_logs", "error", err)
		} else {
			stats["agent_logs_deleted"] = deleted
			s.logger.Info("Purged agent_logs", "deleted", deleted)
		}

		deleted, err = s.maintenanceRepo.CleanupFactHistory(0)
		if err != nil {
			s.logger.Error("Failed to purge fact_history", "error", err)
		} else {
			stats["fact_history_deleted"] = deleted
			s.logger.Info("Purged fact_history", "deleted", deleted)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"stats":   stats,
		"dry_run": req.DryRun,
	})
}

// debugChatSendHandler handles POST /ui/debug/chat/send - sends a test message and returns metrics.
func (s *Server) debugChatSendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req debugChatSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(debugChatSendResponse{Error: "Invalid JSON: " + err.Error()})
		return
	}

	if req.UserID == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(debugChatSendResponse{Error: "user_id is required"})
		return
	}

	if strings.TrimSpace(req.Message) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(debugChatSendResponse{Error: "message is required"})
		return
	}

	// Call bot's SendTestMessage
	result, err := s.bot.SendTestMessage(r.Context(), req.UserID, req.Message, req.SaveToHistory)
	if err != nil {
		s.logger.Error("SendTestMessage failed", "error", err, "user_id", req.UserID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(debugChatSendResponse{Error: err.Error()})
		return
	}

	// Build response
	resp := debugChatSendResponse{
		Response: result.Response,
		CostUSD:  result.TotalCost,
	}
	resp.Timing.TotalMs = result.TimingTotal.Milliseconds()
	resp.Timing.EmbeddingMs = result.TimingEmbedding.Milliseconds()
	resp.Timing.SearchMs = result.TimingSearch.Milliseconds()
	resp.Timing.LLMMs = result.TimingLLM.Milliseconds()
	resp.Tokens.Prompt = result.PromptTokens
	resp.Tokens.Completion = result.CompletionTokens
	resp.Context.TopicsMatched = result.TopicsMatched
	resp.Context.FactsInjected = result.FactsInjected
	resp.Context.Preview = result.ContextPreview

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// agentLogHandler returns a handler for displaying agent logs.
// It's a factory function that creates handlers for different agent types.
func (s *Server) agentLogHandler(agentType, agentName, agentIcon, agentDescription string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pageData, err := s.getCommonData(r)
		if err != nil {
			s.logger.Error("failed to get common data", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if s.agentLogRepo == nil {
			s.logger.Error("agent log repository not configured")
			http.Error(w, "Agent log repository not configured", http.StatusInternalServerError)
			return
		}

		limit := 50
		logs, err := s.agentLogRepo.GetAgentLogs(agentType, pageData.SelectedUserID, limit)
		if err != nil {
			s.logger.Error("failed to get agent logs", "error", err, "agent_type", agentType)
			http.Error(w, "Failed to get agent logs", http.StatusInternalServerError)
			return
		}

		data := struct {
			AgentName        string
			AgentIcon        string
			AgentDescription string
			Logs             []storage.AgentLog
		}{
			AgentName:        agentName,
			AgentIcon:        agentIcon,
			AgentDescription: agentDescription,
			Logs:             logs,
		}

		pageData.Data = data

		if err := s.renderer.Render(w, "agent_log.html", pageData, ui.GetFuncMap()); err != nil {
			s.logger.Error("failed to render template", "error", err)
			http.Error(w, "Failed to render template", http.StatusInternalServerError)
		}
	}
}
