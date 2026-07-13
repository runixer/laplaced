package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/config"
)

const ingestionPipelineVersion = "3"

type ingestionCache struct {
	dir string
}

type ingestionCacheKey struct {
	Version    string               `json:"version"`
	QuestionID string               `json:"question_id"`
	Mode       string               `json:"mode"`
	Sessions   []cacheSession       `json:"sessions"`
	Config     ingestionCacheConfig `json:"config"`
}

type cacheSession struct {
	ID       string         `json:"id"`
	Date     string         `json:"date"`
	Messages []cacheMessage `json:"messages"`
}

type cacheMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cacheAgentRoute struct {
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	Proxy        string `json:"proxy,omitempty"`
	ChatThinking bool   `json:"chat_template_thinking"`
}

type ingestionCacheConfig struct {
	LLMBaseURL     string                       `json:"llm_base_url"`
	Provider       config.ProviderRoutingConfig `json:"provider"`
	ChatBaseURL    string                       `json:"chat_base_url"`
	ChatThinking   bool                         `json:"chat_thinking"`
	RoleRoutes     map[string]cacheAgentRoute   `json:"role_routes,omitempty"`
	SplitterModel  string                       `json:"splitter_model"`
	MergerModel    string                       `json:"merger_model"`
	ArchivistModel string                       `json:"archivist_model"`
	Embedding      config.EmbeddingConfig       `json:"embedding"`
	RAG            config.RAGConfig             `json:"rag"`
	Memory         config.MemoryConfig          `json:"memory"`
	DebugMode      bool                         `json:"debug_mode"`
	Language       string                       `json:"language"`
}

func newIngestionCache(dir string) (*ingestionCache, error) {
	if dir == "" {
		return nil, nil
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve cache directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("stat cache directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cache path is not a directory: %s", absolute)
	}
	return &ingestionCache{dir: absolute}, nil
}

func (c *ingestionCache) key(eval evalCase, sessions []datedSession, mode string, cfg *config.Config, opts options) (string, error) {
	cacheSessions := make([]cacheSession, 0, len(sessions))
	for _, session := range sessions {
		messages := make([]cacheMessage, 0, len(session.Messages))
		for _, message := range session.Messages {
			messages = append(messages, cacheMessage{Role: message.Role, Content: message.Content})
		}
		cacheSessions = append(cacheSessions, cacheSession{ID: session.ID, Date: session.Date.UTC().Format(time.RFC3339Nano), Messages: messages})
	}
	ingestionRoutes := make(map[string]cacheAgentRoute)
	for _, role := range []agent.AgentType{agent.TypeSplitter, agent.TypeArchivist, agent.TypeMerger} {
		if route, ok := opts.roleRoutes[role]; ok {
			ingestionRoutes[string(role)] = cacheAgentRoute(route)
		}
	}
	payload := ingestionCacheKey{
		Version:    ingestionPipelineVersion,
		QuestionID: eval.QuestionID,
		Mode:       mode,
		Sessions:   cacheSessions,
		Config: ingestionCacheConfig{
			LLMBaseURL:     cfg.LLM.BaseURL,
			Provider:       cfg.LLM.Provider,
			ChatBaseURL:    opts.chatBaseURL,
			ChatThinking:   opts.chatThinking,
			RoleRoutes:     ingestionRoutes,
			SplitterModel:  cfg.Agents.Splitter.GetModel(cfg.Agents.Default.Model),
			MergerModel:    cfg.Agents.Merger.GetModel(cfg.Agents.Default.Model),
			ArchivistModel: cfg.Agents.Archivist.GetModel(cfg.Agents.Default.Model),
			Embedding:      cfg.Embedding,
			RAG:            cfg.RAG,
			Memory:         cfg.Memory,
			DebugMode:      cfg.Server.DebugMode,
			Language:       cfg.Bot.Language,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode ingestion cache key: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (c *ingestionCache) materialize(key, destination string) (ingestionSnapshot, bool, error) {
	if c == nil {
		return ingestionSnapshot{}, false, nil
	}
	source := filepath.Join(c.dir, key+".db")
	metadataPath := filepath.Join(c.dir, key+".json")
	if _, err := os.Stat(source); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ingestionSnapshot{}, false, nil
		}
		return ingestionSnapshot{}, false, fmt.Errorf("stat ingestion cache: %w", err)
	}
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ingestionSnapshot{}, false, nil
		}
		return ingestionSnapshot{}, false, fmt.Errorf("read ingestion cache metadata: %w", err)
	}
	var snapshot ingestionSnapshot
	if err := json.Unmarshal(metadata, &snapshot); err != nil {
		return ingestionSnapshot{}, false, nil
	}
	if err := copyFile(source, destination, 0o600); err != nil {
		return ingestionSnapshot{}, false, fmt.Errorf("materialize ingestion cache: %w", err)
	}
	return snapshot, true, nil
}

func (c *ingestionCache) publish(ctx context.Context, key, source string, snapshot ingestionSnapshot) error {
	if c == nil {
		return nil
	}
	finalPath := filepath.Join(c.dir, key+".db")
	metadataPath := filepath.Join(c.dir, key+".json")
	lockPath := filepath.Join(c.dir, key+".lock")
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("acquire ingestion cache lock: %w", err)
		}
		if _, statErr := os.Stat(finalPath); statErr == nil {
			if _, metadataErr := os.Stat(metadataPath); metadataErr == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer os.Remove(lockPath)
	if _, err := os.Stat(finalPath); err == nil {
		if _, metadataErr := os.Stat(metadataPath); metadataErr == nil {
			return nil
		}
		_ = os.Remove(finalPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat ingestion cache destination: %w", err)
	}

	staging, err := os.CreateTemp(c.dir, "."+key+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create ingestion cache staging file: %w", err)
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath)
	input, err := os.Open(source)
	if err != nil {
		_ = staging.Close()
		return fmt.Errorf("open ingestion snapshot: %w", err)
	}
	_, copyErr := io.Copy(staging, input)
	closeInputErr := input.Close()
	syncErr := staging.Sync()
	closeErr := staging.Close()
	if copyErr != nil {
		return fmt.Errorf("copy ingestion snapshot: %w", copyErr)
	}
	if closeInputErr != nil {
		return fmt.Errorf("close ingestion snapshot: %w", closeInputErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync ingestion cache: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close ingestion cache: %w", closeErr)
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		return fmt.Errorf("publish ingestion cache: %w", err)
	}
	if err := os.Chmod(finalPath, 0o400); err != nil {
		return fmt.Errorf("protect ingestion cache: %w", err)
	}
	metadata, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode ingestion cache metadata: %w", err)
	}
	metadataStaging, err := os.CreateTemp(c.dir, "."+key+"-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create ingestion metadata staging file: %w", err)
	}
	metadataStagingPath := metadataStaging.Name()
	defer os.Remove(metadataStagingPath)
	if _, err := metadataStaging.Write(metadata); err != nil {
		_ = metadataStaging.Close()
		return fmt.Errorf("write ingestion cache metadata: %w", err)
	}
	if err := metadataStaging.Sync(); err != nil {
		_ = metadataStaging.Close()
		return fmt.Errorf("sync ingestion cache metadata: %w", err)
	}
	if err := metadataStaging.Close(); err != nil {
		return fmt.Errorf("close ingestion cache metadata: %w", err)
	}
	if err := os.Rename(metadataStagingPath, metadataPath); err != nil {
		return fmt.Errorf("publish ingestion cache metadata: %w", err)
	}
	if err := os.Chmod(metadataPath, 0o400); err != nil {
		return fmt.Errorf("protect ingestion cache metadata: %w", err)
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
