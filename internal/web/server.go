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

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/ui"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	memoryFactsCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "memory_facts_count",
			Help: "Total number of facts in memory",
		},
		[]string{"type"},
	)
	memoryStaleness = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_staleness_days",
			Help: "Average age of facts in days",
		},
	)
	ragLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "rag_latency_seconds",
			Help:    "Latency of RAG operations",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	prometheus.MustRegister(memoryFactsCount)
	prometheus.MustRegister(memoryStaleness)
	prometheus.MustRegister(ragLatency)
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

// Bot is an interface that abstracts the bot's functionality needed by the web server.
type Bot interface {
	HandleUpdateAsync(ctx context.Context, update json.RawMessage, remoteAddr string)
	API() telegram.BotAPI
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
	logRepo         storage.LogRepository
	topicRepo       storage.TopicRepository
	msgRepo         storage.MessageRepository
	factHistoryRepo storage.FactHistoryRepository
	bot             Bot
	rag             *rag.Service
	logger          *slog.Logger
	renderer        *ui.Renderer
	ctx             context.Context // Server's parent context for webhook processing
	statsCache      *dashboardStatsCache
	wg              sync.WaitGroup
}

func NewServer(ctx context.Context, logger *slog.Logger, cfg *config.Config, factRepo storage.FactRepository, userRepo storage.UserRepository, statsRepo storage.StatsRepository, logRepo storage.LogRepository, topicRepo storage.TopicRepository, msgRepo storage.MessageRepository, factHistoryRepo storage.FactHistoryRepository, bot Bot, rag *rag.Service) (*Server, error) {
	renderer, err := ui.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize renderer: %w", err)
	}

	return &Server{
		cfg:             cfg,
		factRepo:        factRepo,
		userRepo:        userRepo,
		statsRepo:       statsRepo,
		logRepo:         logRepo,
		topicRepo:       topicRepo,
		msgRepo:         msgRepo,
		factHistoryRepo: factHistoryRepo,
		bot:             bot,
		rag:             rag,
		logger:          logger.With("component", "web_server"),
		renderer:        renderer,
		statsCache:      newDashboardStatsCache(5 * time.Minute),
		ctx:             ctx,
	}, nil
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
		mux.HandleFunc("/ui/stats", s.statsHandler)
		mux.HandleFunc("/ui/inspector", s.inspectorHandler)
		mux.HandleFunc("/ui/debug/rag", s.debugRAGHandler)
		mux.HandleFunc("/ui/debug/topics", s.topicDebugHandler)
		mux.HandleFunc("/ui/topics", s.topicsHandler)
		mux.HandleFunc("/ui/facts", s.factsHandler)
		mux.HandleFunc("/ui/facts/history", s.factsHistoryHandler)

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
	mux.HandleFunc("/healthz", s.healthzHandler)
	if s.cfg.Telegram.WebhookPath != "" {
		mux.HandleFunc("/telegram/"+s.cfg.Telegram.WebhookPath, s.webhookHandler)
	}
	mux.Handle("/metrics", promhttp.Handler())

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
	stats, err := s.factRepo.GetFactStats()
	if err != nil {
		s.logger.Error("failed to get facts stats for metrics", "error", err)
		return
	}

	for t, count := range stats.CountByType {
		memoryFactsCount.WithLabelValues(t).Set(float64(count))
	}

	memoryStaleness.Set(stats.AvgAgeDays)
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

func (s *Server) inspectorHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	logs, err := s.logRepo.GetRAGLogs(pageData.SelectedUserID, 50) // Last 50 logs
	if err != nil {
		s.logger.Error("failed to get rag logs", "error", err)
		http.Error(w, "Failed to get logs", http.StatusInternalServerError)
		return
	}

	var viewLogs []ui.RAGLogView
	for _, l := range logs {
		viewLogs = append(viewLogs, ui.ParseRAGLog(l))
	}

	pageData.Data = viewLogs

	if err := s.renderer.Render(w, "context_inspector.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *Server) topicsHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	limit := 20
	page := 1
	if pStr := r.URL.Query().Get("page"); pStr != "" {
		_, _ = fmt.Sscanf(pStr, "%d", &page)
		if page < 1 {
			page = 1
		}
	}
	offset := (page - 1) * limit

	var hasFacts *bool
	if val := r.URL.Query().Get("has_facts"); val != "" {
		switch val {
		case "true":
			b := true
			hasFacts = &b
		case "false":
			b := false
			hasFacts = &b
		}
	}

	var isConsolidated *bool
	if val := r.URL.Query().Get("merged"); val != "" {
		switch val {
		case "true":
			b := true
			isConsolidated = &b
		case "false":
			b := false
			isConsolidated = &b
		}
	}

	var topicID *int64
	if val := r.URL.Query().Get("topic_id"); val != "" {
		var id int64
		if _, err := fmt.Sscanf(val, "%d", &id); err == nil {
			topicID = &id
		}
	}

	filter := storage.TopicFilter{
		UserID:         pageData.SelectedUserID,
		Search:         r.URL.Query().Get("q"),
		HasFacts:       hasFacts,
		IsConsolidated: isConsolidated,
		TopicID:        topicID,
	}

	sortBy := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortDir == "" {
		sortDir = "DESC"
	}

	result, err := s.topicRepo.GetTopicsExtended(filter, limit, offset, sortBy, sortDir)
	if err != nil {
		s.logger.Error("failed to get topics", "error", err)
		http.Error(w, "Failed to get topics", http.StatusInternalServerError)
		return
	}

	var viewTopics []ui.TopicView
	for _, t := range result.Data {
		msgs, err := s.msgRepo.GetMessagesInRange(context.Background(), t.UserID, t.StartMsgID, t.EndMsgID)
		if err != nil {
			s.logger.Error("failed to get topic messages", "id", t.ID, "error", err)
			// Continue without messages
		}
		viewTopics = append(viewTopics, ui.TopicView{
			TopicExtended: t,
			Messages:      msgs,
		})
	}

	data := struct {
		Topics  []ui.TopicView
		Total   int
		Page    int
		Limit   int
		Pages   int
		Filter  storage.TopicFilter
		SortBy  string
		SortDir string
	}{
		Topics:  viewTopics,
		Total:   result.TotalCount,
		Page:    page,
		Limit:   limit,
		Pages:   (result.TotalCount + limit - 1) / limit,
		Filter:  filter,
		SortBy:  sortBy,
		SortDir: sortDir,
	}

	pageData.Data = data

	if err := s.renderer.Render(w, "topics.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *Server) debugRAGHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if r.Method == http.MethodGet {
		// If user_id is in query (from getCommonData), use it as default
		data := struct {
			Users            []storage.User
			UserID           int64
			Query            string
			Error            string
			Results          []rag.TopicSearchResult
			EnrichedQuery    string
			EnrichmentPrompt string
		}{
			Users:  pageData.Users,
			UserID: pageData.SelectedUserID,
		}

		if err := s.renderer.Render(w, "rag_debug.html", data, ui.GetFuncMap()); err != nil {
			s.logger.Error("failed to render template", "error", err)
			http.Error(w, "Failed to render template", http.StatusInternalServerError)
		}
		return
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		query := r.FormValue("query")
		userIDStr := r.FormValue("user_id")

		var userID int64 = 0
		_, _ = fmt.Sscanf(userIDStr, "%d", &userID)

		s.logger.Info("Debug RAG search", "user_id", userID, "query", query)

		// Context is currently empty for debug, as parsing text log back to messages is complex
		// and usually we care about "query -> topic" mapping.
		// If needed w could add a simple parser.
		dummyHistory := []storage.Message{}

		skipEnrichment := r.FormValue("skip_enrichment") == "on"

		opts := &rag.RetrievalOptions{
			History:        dummyHistory,
			SkipEnrichment: skipEnrichment,
		}

		results, debugInfo, err := s.rag.Retrieve(r.Context(), userID, query, opts)

		viewData := struct {
			Users            []storage.User
			Query            string
			UserID           int64
			Error            string
			Results          []rag.TopicSearchResult
			EnrichedQuery    string
			EnrichmentPrompt string
		}{
			Users:  pageData.Users,
			Query:  query,
			UserID: userID,
		}

		if err != nil {
			viewData.Error = err.Error()
		} else {
			viewData.Results = results
			if debugInfo != nil {
				viewData.EnrichedQuery = debugInfo.EnrichedQuery
				viewData.EnrichmentPrompt = debugInfo.EnrichmentPrompt
			}
		}

		if err := s.renderer.Render(w, "rag_debug.html", viewData, ui.GetFuncMap()); err != nil {
			s.logger.Error("failed to render template", "error", err)
			http.Error(w, "Failed to render template", http.StatusInternalServerError)
		}
	}
}

func (s *Server) topicDebugHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	limit := 20
	page := 1
	if pStr := r.URL.Query().Get("page"); pStr != "" {
		_, _ = fmt.Sscanf(pStr, "%d", &page)
		if page < 1 {
			page = 1
		}
	}
	offset := (page - 1) * limit

	logs, total, err := s.logRepo.GetTopicExtractionLogs(limit, offset)
	if err != nil {
		s.logger.Error("failed to get topic logs", "error", err)
		http.Error(w, "Failed to get logs", http.StatusInternalServerError)
		return
	}

	var viewLogs []ui.TopicLogView
	for _, l := range logs {
		view := ui.ParseTopicLog(l)

		if view.InputStartID != 0 {
			// Fetch first message to get date
			msgs, err := s.msgRepo.GetMessagesByIDs([]int64{view.InputStartID})
			if err == nil && len(msgs) > 0 {
				view.ChunkDate = msgs[0].CreatedAt
			}
		}

		viewLogs = append(viewLogs, view)
	}

	data := struct {
		Logs  []ui.TopicLogView
		Total int
		Page  int
		Limit int
		Pages int
	}{
		Logs:  viewLogs,
		Total: total,
		Page:  page,
		Limit: limit,
		Pages: (total + limit - 1) / limit,
	}

	pageData.Data = data

	if err := s.renderer.Render(w, "topic_debug.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *Server) factsHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var facts []storage.Fact
	if pageData.SelectedUserID != 0 {
		facts, err = s.factRepo.GetFacts(pageData.SelectedUserID)
	} else {
		facts, err = s.factRepo.GetAllFacts()
	}

	if err != nil {
		s.logger.Error("failed to get facts", "error", err)
		http.Error(w, "Failed to get facts", http.StatusInternalServerError)
		return
	}

	// Sort by Importance desc, then CreatedAt desc
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Importance != facts[j].Importance {
			return facts[i].Importance > facts[j].Importance
		}
		return facts[i].CreatedAt.After(facts[j].CreatedAt)
	})

	pageData.Data = facts

	if err := s.renderer.Render(w, "facts.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *Server) factsHistoryHandler(w http.ResponseWriter, r *http.Request) {
	pageData, err := s.getCommonData(r)
	if err != nil {
		s.logger.Error("failed to get common data", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	limit := 50
	page := 1
	if pStr := r.URL.Query().Get("page"); pStr != "" {
		_, _ = fmt.Sscanf(pStr, "%d", &page)
		if page < 1 {
			page = 1
		}
	}
	offset := (page - 1) * limit

	filter := storage.FactHistoryFilter{
		UserID:   pageData.SelectedUserID,
		Action:   r.URL.Query().Get("action"),
		Category: r.URL.Query().Get("category"),
		Search:   r.URL.Query().Get("search"),
	}

	sortBy := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortDir == "" {
		sortDir = "DESC"
	}

	result, err := s.factHistoryRepo.GetFactHistoryExtended(filter, limit, offset, sortBy, sortDir)
	if err != nil {
		s.logger.Error("failed to get fact history", "error", err)
		http.Error(w, "Failed to get fact history", http.StatusInternalServerError)
		return
	}

	data := struct {
		History []storage.FactHistory
		Total   int
		Page    int
		Limit   int
		Pages   int
		Filter  storage.FactHistoryFilter
		SortBy  string
		SortDir string
	}{
		History: result.Data,
		Total:   result.TotalCount,
		Page:    page,
		Limit:   limit,
		Pages:   (result.TotalCount + limit - 1) / limit,
		Filter:  filter,
		SortBy:  sortBy,
		SortDir: sortDir,
	}

	pageData.Data = data

	if err := s.renderer.Render(w, "facts_history.html", pageData, ui.GetFuncMap()); err != nil {
		s.logger.Error("failed to render template", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
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
