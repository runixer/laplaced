package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTelegramConfig_Getters(t *testing.T) {
	var c TelegramConfig
	assert.Equal(t, "https://api.telegram.org", c.GetAPIBaseURL())
	assert.Equal(t, "/var/lib/telegram-bot-api", c.GetLocalFilesDir())
	assert.Equal(t, int64(20<<20), c.GetMaxDownloadBytes())
	assert.Equal(t, 50<<20, c.GetMaxUploadBytes())

	c = TelegramConfig{APIBaseURL: " http://bot-api:8081/ ", LocalFilesDir: "/srv/tg", MaxDownloadMB: 200, MaxUploadMB: 2000}
	assert.Equal(t, "http://bot-api:8081", c.GetAPIBaseURL())
	assert.Equal(t, "/srv/tg", c.GetLocalFilesDir())
	assert.Equal(t, int64(200<<20), c.GetMaxDownloadBytes())
	assert.Equal(t, 2000<<20, c.GetMaxUploadBytes())
}

func TestValidateTelegramLimits(t *testing.T) {
	local := func(c *TelegramConfig) {
		c.LocalMode = true
		c.APIBaseURL = "http://bot-api:8081"
	}
	tests := []struct {
		name    string
		mutate  func(*TelegramConfig)
		wantErr string
	}{
		{name: "public defaults", mutate: func(*TelegramConfig) {}},
		{name: "public caps at their limits", mutate: func(c *TelegramConfig) { c.MaxDownloadMB, c.MaxUploadMB = 20, 50 }},
		{name: "download above 20 needs local mode", mutate: func(c *TelegramConfig) { c.MaxDownloadMB = 21 }, wantErr: "max_download_mb above 20 requires telegram.local_mode"},
		{name: "upload above 50 needs local mode", mutate: func(c *TelegramConfig) { c.MaxUploadMB = 51 }, wantErr: "max_upload_mb above 50 requires telegram.local_mode"},
		{name: "negative download", mutate: func(c *TelegramConfig) { c.MaxDownloadMB = -1 }, wantErr: "max_download_mb must not be negative"},
		{name: "negative upload", mutate: func(c *TelegramConfig) { c.MaxUploadMB = -1 }, wantErr: "max_upload_mb must not be negative"},
		{name: "local mode lifts both caps", mutate: func(c *TelegramConfig) { local(c); c.MaxDownloadMB, c.MaxUploadMB = 500, 2000 }},
		{name: "upload above 2000 is rejected even locally", mutate: func(c *TelegramConfig) { local(c); c.MaxUploadMB = 2001 }, wantErr: "must not exceed 2000"},
		{name: "local mode needs a self-hosted URL", mutate: func(c *TelegramConfig) { c.LocalMode = true }, wantErr: "api_base_url pointing at a self-hosted"},
		{name: "local files dir must be absolute", mutate: func(c *TelegramConfig) { local(c); c.LocalFilesDir = "data/tg" }, wantErr: "local_files_dir must be an absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Transport: "telegram"}
			cfg.Telegram.Token = "test"
			tt.mutate(&cfg.Telegram)
			errs := cfg.validateTransport()
			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			if assert.Len(t, errs, 1) {
				assert.Contains(t, errs[0].Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateTelegramLimits_SkippedForMattermost(t *testing.T) {
	cfg := &Config{Transport: "mattermost"}
	cfg.Mattermost.ServerURL = "https://chat.example.com"
	cfg.Mattermost.BotToken = "x"
	cfg.Telegram.MaxUploadMB = 5000
	assert.Empty(t, cfg.validateTransport())
}
