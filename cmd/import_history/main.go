package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
)

// Telegram Export Structs
type ExportMessage struct {
	ID            int64           `json:"id"`
	Type          string          `json:"type"`
	Date          string          `json:"date"`
	From          string          `json:"from"`
	FromID        string          `json:"from_id"`
	ForwardedFrom string          `json:"forwarded_from"`
	Text          json.RawMessage `json:"text"` // string or array of entities
	Photo         string          `json:"photo"`
	File          string          `json:"file"`
	MediaType     string          `json:"media_type"`
	StickerEmoji  string          `json:"sticker_emoji"`
}

func main() {
	filePath := flag.String("file", "result.json", "path to result.json file")
	targetUserID := flag.Int64("user_id", 0, "target Telegram User ID")
	clear := flag.Bool("clear", false, "clear history before import")
	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if *targetUserID == 0 {
		// Try to read config to find default user
		cfg, err := config.Load(*configPath)
		if err == nil && len(cfg.Bot.AllowedUserIDs) > 0 {
			if len(cfg.Bot.AllowedUserIDs) == 1 {
				*targetUserID = cfg.Bot.AllowedUserIDs[0]
				logger.Info("auto-detected user_id from config", "user_id", *targetUserID)
			} else {
				logger.Error("multiple allowed users in config, please specify -user_id")
				flag.Usage()
				os.Exit(1)
			}
		} else {
			logger.Error("user_id is required")
			flag.Usage()
			os.Exit(1)
		}
	}

	// Init DB
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	store, err := storage.NewSQLiteStore(logger, cfg.Database.Path)
	if err != nil {
		logger.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Init(); err != nil {
		logger.Error("failed to init db", "error", err)
		os.Exit(1)
	}

	if *clear {
		if err := store.ClearHistory(*targetUserID); err != nil {
			logger.Error("failed to clear history", "error", err)
			os.Exit(1)
		}
		logger.Info("history cleared")
	}

	// Read JSON Stream
	file, err := os.Open(*filePath)
	if err != nil {
		logger.Error("failed to open file", "error", err)
		os.Exit(1)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)

	// Read opening brace
	if _, err := decoder.Token(); err != nil {
		logger.Error("failed to read opening brace", "error", err)
		os.Exit(1)
	}

	// Find "messages" key
	for decoder.More() {
		t, err := decoder.Token()
		if err != nil {
			logger.Error("failed to read token", "error", err)
			os.Exit(1)
		}
		if key, ok := t.(string); ok && key == "messages" {
			break
		}
	}

	// Read opening bracket of messages array
	if _, err := decoder.Token(); err != nil {
		logger.Error("failed to read opening bracket", "error", err)
		os.Exit(1)
	}

	count := 0
	skipped := 0
	targetUserStr := fmt.Sprintf("user%d", *targetUserID)

	// Iterate over messages
	for decoder.More() {
		var msg ExportMessage
		if err := decoder.Decode(&msg); err != nil {
			logger.Error("failed to decode message", "error", err)
			continue
		}

		if msg.Type != "message" {
			continue
		}

		// Convert Date
		createdAt, err := time.Parse("2006-01-02T15:04:05", msg.Date)
		if err != nil {
			// Try parsing with timezone if standard fails, though usually it's without Z
			// Some exports might have different formats.
			logger.Warn("failed to parse date, using current", "date", msg.Date, "error", err)
			createdAt = time.Now()
		}

		// Determine Role
		role := "assistant"
		if msg.FromID == targetUserStr {
			role = "user"
		}

		// Extract Content
		content := extractText(msg.Text)

		if msg.ForwardedFrom != "" {
			// Ensure we preserve the forwarded info clearly
			header := fmt.Sprintf("Forwarded from %s", msg.ForwardedFrom)
			if content != "" {
				content = fmt.Sprintf("[%s]:\n%s", header, content)
			} else {
				content = fmt.Sprintf("[%s]", header)
			}
		}

		// If content is empty (e.g. photo without caption), try to add a placeholder
		if content == "" {
			if msg.Photo != "" {
				content = "[Photo]"
			} else if msg.File != "" {
				content = fmt.Sprintf("[File: %s]", msg.File)
			} else if msg.StickerEmoji != "" {
				content = fmt.Sprintf("[Sticker: %s]", msg.StickerEmoji)
			} else {
				skipped++
				continue
			}
		}

		dbMsg := storage.Message{
			Role:      role,
			Content:   content,
			CreatedAt: createdAt,
		}

		if err := store.ImportMessage(*targetUserID, dbMsg); err != nil {
			logger.Error("failed to import message", "id", msg.ID, "error", err)
		} else {
			count++
		}

		if count%1000 == 0 {
			logger.Info("progress", "imported", count)
		}
	}

	// Read closing bracket and brace (optional, but good practice)
	_, _ = decoder.Token()
	_, _ = decoder.Token()

	logger.Info("import completed", "imported", count, "skipped", skipped)
}

func extractText(raw json.RawMessage) string {
	// Try string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try array of objects/strings
	var parts []interface{}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			switch v := p.(type) {
			case string:
				sb.WriteString(v)
			case map[string]interface{}:
				if t, ok := v["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}

	return ""
}
