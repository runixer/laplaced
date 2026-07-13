// Command longmemeval runs a small LongMemEval dataset through Laplaced's
// production memory and answer pipeline.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/app"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/secrets"
	"github.com/runixer/laplaced/internal/storage"
	"golang.org/x/sync/errgroup"
)

type options struct {
	dataset      string
	output       string
	config       string
	mode         string
	caseID       string
	limit        int
	verbose      bool
	chatBaseURL  string
	chatModel    string
	chatProxy    string
	chatThinking bool
	matrix       string
	parallel     int
	judge        bool
	judgeModel   string
	cacheDir     string
	roleRoutes   map[agent.AgentType]matrixAgentRoute
}

type runResult struct {
	Variant         string             `json:"variant"`
	QuestionID      string             `json:"question_id"`
	Hypothesis      string             `json:"hypothesis"`
	QuestionType    string             `json:"question_type,omitempty"`
	ReferenceAnswer string             `json:"reference_answer,omitempty"`
	Mode            string             `json:"mode"`
	ChatBackend     string             `json:"chat_backend"`
	ChatModel       string             `json:"chat_model"`
	QuestionDate    string             `json:"question_date,omitempty"`
	Sessions        int                `json:"sessions"`
	TotalDurationMS int64              `json:"total_duration_ms"`
	Ingestion       aggregateStats     `json:"ingestion"`
	Answer          answerStats        `json:"answer"`
	Facts           []factSnapshot     `json:"facts,omitempty"`
	FactChanges     []factChange       `json:"fact_changes,omitempty"`
	AutoevalLabel   *autoevalLabel     `json:"autoeval_label,omitempty"`
	Judge           *judgeStats        `json:"judge,omitempty"`
	CacheHit        bool               `json:"cache_hit"`
	Retrieval       *retrievalEvidence `json:"retrieval,omitempty"`
}

type ingestionSnapshot struct {
	Stats    aggregateStats    `json:"stats"`
	Sessions []importedSession `json:"sessions"`
}

type aggregateStats struct {
	DurationMS       int64   `json:"duration_ms"`
	Messages         int     `json:"messages"`
	Topics           int     `json:"topics"`
	FactsCreated     int     `json:"facts_created"`
	FactsUpdated     int     `json:"facts_updated"`
	FactsDeleted     int     `json:"facts_deleted"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	EmbeddingTokens  int     `json:"embedding_tokens"`
	Cost             float64 `json:"cost_usd"`
}

type factSnapshot struct {
	Content    string `json:"content"`
	Category   string `json:"category"`
	Type       string `json:"type"`
	Kind       string `json:"kind"`
	Importance int    `json:"importance"`
}

type factChange struct {
	Action     string `json:"action"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type answerStats struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost_usd"`
	DurationMS       int64   `json:"duration_ms"`
	TopicsMatched    int     `json:"topics_matched"`
	FactsInjected    int     `json:"facts_injected"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "longmemeval:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	cases, err := loadDataset(opts.dataset)
	if err != nil {
		return err
	}
	cases = filterCases(cases, opts.caseID, opts.limit)
	if len(cases) == 0 {
		return errors.New("no matching evaluation cases")
	}

	variants, err := resolveVariants(opts)
	if err != nil {
		return err
	}

	writer, closeWriter, err := resultWriter(opts.output)
	if err != nil {
		return err
	}
	defer closeWriter()
	encoder := json.NewEncoder(writer)
	var outputMu sync.Mutex

	group, groupCtx := errgroup.WithContext(ctx)
	semaphore := make(chan struct{}, opts.parallel)
	for _, variant := range variants {
		variant := variant
		group.Go(func() error {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-groupCtx.Done():
				return groupCtx.Err()
			}
			return runVariant(groupCtx, opts, variant, cases, func(result *runResult) error {
				outputMu.Lock()
				defer outputMu.Unlock()
				return encoder.Encode(result)
			})
		})
	}
	return group.Wait()
}

func resolveVariants(opts options) ([]matrixVariant, error) {
	if opts.matrix != "" {
		return loadMatrix(opts.matrix)
	}
	return []matrixVariant{{Name: "default", ChatBaseURL: opts.chatBaseURL, ChatModel: opts.chatModel, ChatProxy: opts.chatProxy, ChatThinking: opts.chatThinking}}, nil
}

func runVariant(ctx context.Context, base options, variant matrixVariant, cases []evalCase, writeResult func(*runResult) error) error {
	opts := variant.apply(base)
	baseCfg, logger, err := loadRuntimeConfig(ctx, opts)
	if err != nil {
		return fmt.Errorf("variant %s: %w", variant.Name, err)
	}
	baseCfg.Bot.Language = "en"
	baseCfg.Artifacts.Enabled = false
	if opts.chatBaseURL != "" {
		overrideChatModels(baseCfg, opts.chatModel)
	}
	applyRoleModels(baseCfg, opts.roleRoutes)
	cache, err := newIngestionCache(opts.cacheDir)
	if err != nil {
		return fmt.Errorf("variant %s: %w", variant.Name, err)
	}

	for i, eval := range cases {
		logger.Info("running evaluation case", "variant", variant.Name, "index", i+1, "total", len(cases), "question_id", eval.QuestionID)
		result, err := runCachedCase(ctx, baseCfg, logger, opts, cache, eval, base.mode)
		if err != nil {
			return fmt.Errorf("variant %s case %s: %w", variant.Name, eval.QuestionID, err)
		}
		result.Variant = variant.Name
		if err := writeResult(result); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}
	return nil
}

func runCachedCase(ctx context.Context, baseCfg *config.Config, logger *slog.Logger, opts options, cache *ingestionCache, eval evalCase, mode string) (*runResult, error) {
	startedAt := time.Now()
	sessions, err := selectSessions(eval, mode)
	if err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp("", "laplaced-longmemeval-case-*")
	if err != nil {
		return nil, fmt.Errorf("create case directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	dbPath := filepath.Join(tempDir, "eval.db")
	key := ""
	cacheHit := false
	var ingestion ingestionSnapshot
	if cache != nil {
		key, err = cache.key(eval, sessions, mode, baseCfg, opts)
		if err != nil {
			return nil, err
		}
		ingestion, cacheHit, err = cache.materialize(key, dbPath)
		if err != nil {
			return nil, err
		}
		if cacheHit {
			ingestion.Stats = aggregateStats{}
		}
	}

	if !cacheHit {
		ingestCfg := *baseCfg
		ingestRuntime, runtimeErr := newEvalRuntime(ctx, &ingestCfg, logger, opts, dbPath)
		if runtimeErr != nil {
			return nil, runtimeErr
		}
		ingestion, err = ingestCase(ctx, ingestRuntime, eval, sessions)
		if err == nil {
			err = ingestRuntime.store.Checkpoint()
		}
		closeErr := ingestRuntime.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close ingestion runtime: %w", closeErr)
		}
		if cache != nil {
			if err := cache.publish(ctx, key, dbPath, ingestion); err != nil {
				return nil, err
			}
		}
	}

	answerCfg := *baseCfg
	answerRuntime, err := newEvalRuntime(ctx, &answerCfg, logger, opts, dbPath)
	if err != nil {
		return nil, err
	}
	defer answerRuntime.Close()
	result, err := answerCase(ctx, answerRuntime, eval, mode, opts, len(sessions), ingestion, startedAt)
	if err != nil {
		return nil, err
	}
	result.CacheHit = cacheHit
	return result, nil
}

func ingestCase(ctx context.Context, runtime *evalRuntime, eval evalCase, sessions []datedSession) (ingestionSnapshot, error) {
	scopeID := storage.PassthroughScopeID("longmemeval", eval.QuestionID)
	if err := runtime.store.UpsertUser(storage.User{ID: scopeID, Username: "eval-user", FirstName: "Eval", LastSeen: time.Now()}); err != nil {
		return ingestionSnapshot{}, fmt.Errorf("create evaluation user: %w", err)
	}
	var ingestion ingestionSnapshot
	startedAt := time.Now()
	for _, session := range sessions {
		stats, mapping, err := runtime.ingestSession(ctx, scopeID, session)
		if err != nil {
			return ingestion, err
		}
		ingestion.Stats.add(stats)
		ingestion.Sessions = append(ingestion.Sessions, mapping)
	}
	ingestion.Stats.DurationMS = time.Since(startedAt).Milliseconds()
	return ingestion, nil
}

func answerCase(ctx context.Context, runtime *evalRuntime, eval evalCase, mode string, opts options, sessionCount int, ingestion ingestionSnapshot, startedAt time.Time) (*runResult, error) {
	scopeID := storage.PassthroughScopeID("longmemeval", eval.QuestionID)
	if err := runtime.store.UpsertUser(storage.User{ID: scopeID, Username: "eval-user", FirstName: "Eval", LastSeen: time.Now()}); err != nil {
		return nil, fmt.Errorf("load evaluation user: %w", err)
	}
	questionDate, err := parseDatasetTime(eval.QuestionDate)
	if err != nil {
		return nil, fmt.Errorf("question date: %w", err)
	}
	answerCtx := agent.WithReferenceTime(ctx, questionDate)
	answer, err := runtime.bot.SendTestMessage(answerCtx, scopeID, eval.Question, false)
	if err != nil {
		return nil, fmt.Errorf("answer question: %w", err)
	}
	facts, err := runtime.store.GetFacts(scopeID)
	if err != nil {
		return nil, fmt.Errorf("load final facts: %w", err)
	}
	history, err := runtime.store.GetFactHistory(scopeID, 1000)
	if err != nil {
		return nil, fmt.Errorf("load fact history: %w", err)
	}
	retrieval, err := calculateRetrievalEvidence(runtime.store, scopeID, eval, ingestion, answer.RAGDebugInfo)
	if err != nil {
		return nil, err
	}
	var label *autoevalLabel
	var judge *judgeStats
	if runtime.judge != nil {
		label, judge, err = runtime.judge.Evaluate(ctx, eval, answer.Response)
		if err != nil {
			return nil, fmt.Errorf("judge answer: %w", err)
		}
	}
	return &runResult{
		QuestionID: eval.QuestionID, Hypothesis: answer.Response, QuestionType: eval.QuestionType,
		ReferenceAnswer: string(eval.Answer), Mode: mode, ChatBackend: chatBackend(opts), ChatModel: chatModel(runtime, opts), QuestionDate: eval.QuestionDate,
		Sessions: sessionCount, TotalDurationMS: time.Since(startedAt).Milliseconds(), Ingestion: ingestion.Stats,
		Answer:        answerStats{PromptTokens: answer.PromptTokens, CompletionTokens: answer.CompletionTokens, Cost: answer.TotalCost, DurationMS: answer.TimingTotal.Milliseconds(), TopicsMatched: answer.TopicsMatched, FactsInjected: answer.FactsInjected},
		Facts:         snapshotFacts(facts),
		FactChanges:   snapshotFactChanges(history),
		AutoevalLabel: label,
		Judge:         judge,
		Retrieval:     retrieval,
	}, nil
}

func applyRoleModels(cfg *config.Config, routes map[agent.AgentType]matrixAgentRoute) {
	for role, route := range routes {
		switch role {
		case agent.TypeSplitter:
			cfg.Agents.Splitter.Model = route.Model
		case agent.TypeArchivist:
			cfg.Agents.Archivist.Model = route.Model
		case agent.TypeMerger:
			cfg.Agents.Merger.Model = route.Model
		case agent.TypeEnricher:
			cfg.Agents.Enricher.Model = route.Model
		case agent.TypeReranker:
			cfg.Agents.Reranker.Model = route.Model
		case agent.TypeLaplace:
			cfg.Agents.Chat.Model = route.Model
			cfg.Agents.ChatModel = route.Model
		}
	}
}

func chatBackend(opts options) string {
	if opts.chatBaseURL != "" {
		return opts.chatBaseURL
	}
	return "configured"
}

func chatModel(runtime *evalRuntime, opts options) string {
	if opts.chatModel != "" {
		return opts.chatModel
	}
	return runtime.cfg.Agents.GetChatModel()
}

func snapshotFacts(facts []storage.Fact) []factSnapshot {
	result := make([]factSnapshot, 0, len(facts))
	for _, fact := range facts {
		result = append(result, factSnapshot{Content: fact.Content, Category: fact.Category, Type: fact.Type, Kind: fact.Kind, Importance: fact.Importance})
	}
	return result
}

func snapshotFactChanges(history []storage.FactHistory) []factChange {
	result := make([]factChange, 0, len(history))
	for _, change := range history {
		result = append(result, factChange{Action: change.Action, OldContent: change.OldContent, NewContent: change.NewContent, Reason: change.Reason})
	}
	return result
}

func (s *aggregateStats) add(stats *rag.ProcessingStats) {
	s.Messages += stats.MessagesProcessed
	s.Topics += stats.TopicsExtracted
	s.FactsCreated += stats.FactsCreated
	s.FactsUpdated += stats.FactsUpdated
	s.FactsDeleted += stats.FactsDeleted
	s.PromptTokens += stats.PromptTokens
	s.CompletionTokens += stats.CompletionTokens
	s.EmbeddingTokens += stats.EmbeddingTokens
	if stats.TotalCost != nil {
		s.Cost += *stats.TotalCost
	}
}

func parseOptions(args []string) (options, error) {
	var opts options
	set := flag.NewFlagSet("longmemeval", flag.ContinueOnError)
	set.StringVar(&opts.dataset, "dataset", "", "Path to a LongMemEval JSON dataset")
	set.StringVar(&opts.output, "output", "-", "JSONL output path, or - for stdout")
	set.StringVar(&opts.config, "config", "", "Path to config YAML")
	set.StringVar(&opts.mode, "mode", "oracle", "Session mode: oracle or full")
	set.StringVar(&opts.caseID, "case", "", "Run only one question_id")
	set.IntVar(&opts.limit, "limit", 10, "Maximum number of cases; 0 means all")
	set.BoolVar(&opts.verbose, "verbose", false, "Enable debug logs on stderr")
	set.StringVar(&opts.chatBaseURL, "chat-base-url", "", "Optional OpenAI-compatible endpoint for chat calls only")
	set.StringVar(&opts.chatModel, "chat-model", "", "Model used for all chat agents with --chat-base-url")
	set.StringVar(&opts.chatProxy, "chat-proxy", "", "Optional proxy for the chat-only endpoint")
	set.BoolVar(&opts.chatThinking, "chat-thinking", false, "Enable thinking through chat_template_kwargs on the chat-only endpoint")
	set.StringVar(&opts.matrix, "matrix", "", "YAML file containing evaluation variants")
	set.IntVar(&opts.parallel, "parallel", 1, "Maximum matrix variants to run concurrently")
	set.BoolVar(&opts.judge, "judge", false, "Judge generated answers with the official LongMemEval V1 protocol")
	set.StringVar(&opts.judgeModel, "judge-model", defaultJudgeModel, "Model used by the LongMemEval judge")
	set.StringVar(&opts.cacheDir, "cache-dir", "", "Directory for immutable per-case ingestion snapshots")
	if err := set.Parse(args); err != nil {
		return opts, err
	}
	if opts.dataset == "" {
		return opts, errors.New("--dataset is required")
	}
	if opts.mode != "oracle" && opts.mode != "full" {
		return opts, fmt.Errorf("unsupported --mode %q", opts.mode)
	}
	if opts.limit < 0 {
		return opts, errors.New("--limit cannot be negative")
	}
	if opts.parallel < 1 {
		return opts, errors.New("--parallel must be at least 1")
	}
	if opts.judge && strings.TrimSpace(opts.judgeModel) == "" {
		return opts, errors.New("--judge-model cannot be empty when --judge is enabled")
	}
	if (opts.chatBaseURL == "") != (opts.chatModel == "") {
		return opts, errors.New("--chat-base-url and --chat-model must be used together")
	}
	if opts.chatThinking && opts.chatBaseURL == "" {
		return opts, errors.New("--chat-thinking requires --chat-base-url")
	}
	if opts.matrix != "" && (opts.chatBaseURL != "" || opts.chatModel != "" || opts.chatProxy != "" || opts.chatThinking) {
		return opts, errors.New("--matrix cannot be combined with chat override flags")
	}
	return opts, nil
}

func filterCases(cases []evalCase, caseID string, limit int) []evalCase {
	filtered := make([]evalCase, 0, len(cases))
	for _, eval := range cases {
		if caseID != "" && eval.QuestionID != caseID {
			continue
		}
		filtered = append(filtered, eval)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func loadRuntimeConfig(ctx context.Context, opts options) (*config.Config, *slog.Logger, error) {
	_ = app.LoadEnv()
	cfg, err := config.Load(opts.config)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	var handler slog.Handler = slog.NewTextHandler(io.Discard, nil)
	if opts.verbose {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	logger := slog.New(handler)
	secretCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var provider config.SecretProvider
	if cfg.Vault != nil {
		provider, err = secrets.New(secretCtx, *cfg.Vault, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize secret provider: %w", err)
		}
	}
	if err := cfg.ResolveSecrets(secretCtx, provider); err != nil {
		return nil, nil, fmt.Errorf("resolve secrets: %w", err)
	}
	if strings.TrimSpace(cfg.LLM.APIKey) == "" {
		return nil, nil, errors.New("LAPLACED_LLM_API_KEY is not configured")
	}
	return cfg, logger, nil
}

func resultWriter(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("create output: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}
