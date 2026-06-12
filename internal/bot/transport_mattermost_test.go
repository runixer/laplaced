package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestMMRenderer_PassThroughAndSplit(t *testing.T) {
	r := NewMattermostRenderer(16383, testutil.TestLogger())

	t.Run("markdown passes through unescaped", func(t *testing.T) {
		chunks, err := r.Render(context.Background(), "**bold** and `code` and <not html>")
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(chunks) != 1 {
			t.Fatalf("want 1 chunk, got %d", len(chunks))
		}
		// No HTML conversion: raw markdown + literal angle brackets survive.
		if !strings.Contains(chunks[0], "**bold**") || !strings.Contains(chunks[0], "<not html>") {
			t.Errorf("markdown was altered: %q", chunks[0])
		}
	})

	t.Run("splits on ###SPLIT### delimiter", func(t *testing.T) {
		chunks, err := r.Render(context.Background(), "part one###SPLIT###part two")
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(chunks) != 2 {
			t.Fatalf("want 2 chunks, got %d: %v", len(chunks), chunks)
		}
	})

	t.Run("blank chunks dropped", func(t *testing.T) {
		chunks, _ := r.Render(context.Background(), "   ")
		if len(chunks) != 0 {
			t.Errorf("want 0 chunks for blank input, got %d", len(chunks))
		}
	})
}

func TestMMRenderer_RespectsMaxLen(t *testing.T) {
	r := NewMattermostRenderer(300, testutil.TestLogger()) // tiny limit
	long := strings.Repeat("word ", 200)                   // ~1000 chars
	chunks, err := r.Render(context.Background(), long)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the long text to be split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 300 {
			t.Errorf("chunk %d length %d exceeds maxLen", i, len(c))
		}
	}
}

func TestMMTransport_IsAllowed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Mattermost.AllowedUserIDs = []string{"alice26charxxxxxxxxxxxxxxx", "bob26charxxxxxxxxxxxxxxxxx"}
	tr := &MMTransport{cfg: cfg}

	if !tr.IsAllowed("alice26charxxxxxxxxxxxxxxx") {
		t.Error("allowed user should pass")
	}
	if tr.IsAllowed("eve26charxxxxxxxxxxxxxxxxx") {
		t.Error("non-allowed user should be rejected")
	}
	if tr.IsAllowed("") {
		t.Error("empty sender should be rejected")
	}

	// Empty allowlist fails closed.
	if (&MMTransport{cfg: &config.Config{}}).IsAllowed("anyone") {
		t.Error("empty allowlist should reject everyone")
	}
}

// TestBot_IsAllowedScope covers the transport-agnostic download authorization:
// a scope id must match an allowlisted user on EITHER transport, derived via the
// same PassthroughScopeID the ingestion path uses.
func TestBot_IsAllowedScope(t *testing.T) {
	cfg := &config.Config{}
	cfg.Bot.AllowedUserIDs = []int64{123}
	cfg.Mattermost.AllowedUserIDs = []string{"bk7w9enuabyp9gmr6fw8t91exy"}
	b := &Bot{cfg: cfg}

	tgScope := storage.PassthroughScopeID(transportTelegram, "123")
	mmScope := storage.PassthroughScopeID(transportMattermost, "bk7w9enuabyp9gmr6fw8t91exy")

	if !b.IsAllowedScope(tgScope) {
		t.Error("telegram-allowlisted scope should be allowed")
	}
	if !b.IsAllowedScope(mmScope) {
		t.Error("mattermost-allowlisted scope should be allowed")
	}
	if b.IsAllowedScope(storage.PassthroughScopeID(transportMattermost, "evexxxxxxxxxxxxxxxxxxxxxxxx")) {
		t.Error("non-allowlisted scope should be rejected")
	}
	if b.IsAllowedScope("not-a-real-scope") {
		t.Error("arbitrary scope should be rejected")
	}
}

func TestMMTransport_Kind(t *testing.T) {
	tr := &MMTransport{}
	if tr.Kind() != transportMattermost {
		t.Errorf("Kind() = %q, want %q", tr.Kind(), transportMattermost)
	}
}

func TestMMTransport_SendText_ThreadRootFallback(t *testing.T) {
	var gotBody struct {
		ChannelID      string `json:"channel_id"`
		Message        string `json:"message"`
		RootID         string `json:"root_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "botid", "username": "laplaced", "is_bot": true})
		case "/api/v4/config/client":
			_ = json.NewEncoder(w).Encode(map[string]string{"MaxPostSize": "16383"})
		case "/api/v4/posts":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sentpost"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := mattermost.NewClient(context.Background(), mattermost.Config{ServerURL: srv.URL, BotToken: "tok"}, testutil.TestLogger())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tr := NewMattermostTransport(client, &config.Config{}, testutil.TestLogger())

	t.Run("ThreadRoot used directly", func(t *testing.T) {
		id, err := tr.SendText(context.Background(), OutgoingResponse{ConversationID: "c1", Text: "hi", ThreadRoot: "root1", ReplyTo: "reply1"})
		if err != nil {
			t.Fatalf("SendText: %v", err)
		}
		if id != "sentpost" {
			t.Errorf("returned id = %q, want sentpost", id)
		}
		if gotBody.RootID != "root1" {
			t.Errorf("root_id = %q, want root1 (ThreadRoot wins)", gotBody.RootID)
		}
		if gotBody.IdempotencyKey == "" {
			t.Error("idempotency_key should be set")
		}
	})

	t.Run("falls back to ReplyTo when ThreadRoot empty", func(t *testing.T) {
		_, err := tr.SendText(context.Background(), OutgoingResponse{ConversationID: "c1", Text: "hi", ThreadRoot: "", ReplyTo: "reply1"})
		if err != nil {
			t.Fatalf("SendText: %v", err)
		}
		if gotBody.RootID != "reply1" {
			t.Errorf("root_id = %q, want reply1 (fallback)", gotBody.RootID)
		}
	})
}

func TestIncomingFromMattermost_Mapping(t *testing.T) {
	// Test server serves bot identity + per-user profiles so the envelope's
	// sender display/prefix (the #4 identity feature) can be asserted.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "botid", "username": "laplaced", "is_bot": true})
		case "/api/v4/config/client":
			_ = json.NewEncoder(w).Encode(map[string]string{"MaxPostSize": "16383"})
		case "/api/v4/users/user1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user1", "username": "jdoe", "first_name": "John", "last_name": "Doe"})
		case "/api/v4/users/user2":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user2", "username": "alice"})
		case "/api/v4/channels/channamed":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "channamed", "name": "release-team", "display_name": "Release Team", "type": "O"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := mattermost.NewClient(context.Background(), mattermost.Config{ServerURL: srv.URL, BotToken: "tok"}, testutil.TestLogger())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	b := &Bot{logger: testutil.TestLogger()}

	t.Run("top-level DM threads under its own post id and carries sender prefix", func(t *testing.T) {
		ev := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "post1", UserID: "user1", ChannelID: "chan1", RootID: "", Message: "hello", CreateAt: 1735732800000},
			ChannelType: "D",
		}
		im := b.incomingFromMattermost(client, ev)
		if im.ConversationID != "chan1" || im.SenderID != "user1" || im.MessageID != "post1" || im.Text != "hello" {
			t.Errorf("envelope core fields wrong: %+v", im)
		}
		if !im.IsDirect {
			t.Error("channel_type D should map to IsDirect=true")
		}
		if im.ThreadRoot != "post1" {
			t.Errorf("ThreadRoot = %q, want post1 (top-level threads on own id)", im.ThreadRoot)
		}
		if im.SenderDisplay != "John Doe (@jdoe)" {
			t.Errorf("SenderDisplay = %q, want \"John Doe (@jdoe)\"", im.SenderDisplay)
		}
		if !strings.HasPrefix(im.Prefix, "[John Doe (@jdoe) (") {
			t.Errorf("Prefix = %q, want it to start with the resolved name", im.Prefix)
		}
		if im.Files != nil {
			t.Error("PoC is text-only; Files should be nil")
		}
	})

	t.Run("reply preserves existing root, non-DM channel, username-only sender", func(t *testing.T) {
		ev := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "post2", UserID: "user2", ChannelID: "chan2", RootID: "rootX", Message: "reply"},
			ChannelType: "O",
		}
		im := b.incomingFromMattermost(client, ev)
		if im.ThreadRoot != "rootX" {
			t.Errorf("ThreadRoot = %q, want rootX", im.ThreadRoot)
		}
		if im.IsDirect {
			t.Error("channel_type O should map to IsDirect=false")
		}
		if im.SenderDisplay != "@alice" {
			t.Errorf("SenderDisplay = %q, want \"@alice\" (username-only fallback)", im.SenderDisplay)
		}
	})

	t.Run("falls back to raw id when profile fetch fails", func(t *testing.T) {
		ev := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "post3", UserID: "ghost", ChannelID: "chan3", Message: "boo"},
			ChannelType: "D",
		}
		im := b.incomingFromMattermost(client, ev)
		if im.SenderDisplay != "ghost" {
			t.Errorf("SenderDisplay = %q, want raw id \"ghost\" on fetch failure", im.SenderDisplay)
		}
	})

	t.Run("ReplyToBot set when the post quotes a bot message", func(t *testing.T) {
		quotesBot := mattermost.Post{ID: "q1", UserID: "user2", ChannelID: "chan2", Message: "what about this?"}
		quotesBot.Metadata.Embeds = []mattermost.Embed{{Type: "quote"}}
		quotesBot.Metadata.Embeds[0].Data.Post.UserID = "botid"
		if im := b.incomingFromMattermost(client, mattermost.PostedEvent{Post: quotesBot, ChannelType: "O"}); !im.ReplyToBot {
			t.Error("ReplyToBot should be true when the quoted post is the bot's")
		}

		quotesUser := mattermost.Post{ID: "q2", UserID: "user2", ChannelID: "chan2", Message: "@user1 see this"}
		quotesUser.Metadata.Embeds = []mattermost.Embed{{Type: "quote"}}
		quotesUser.Metadata.Embeds[0].Data.Post.UserID = "user1"
		if im := b.incomingFromMattermost(client, mattermost.PostedEvent{Post: quotesUser, ChannelType: "O"}); im.ReplyToBot {
			t.Error("ReplyToBot should be false when the quoted post is another user's")
		}

		plain := mattermost.Post{ID: "q3", UserID: "user2", ChannelID: "chan2", Message: "just chatting"}
		if im := b.incomingFromMattermost(client, mattermost.PostedEvent{Post: plain, ChannelType: "O"}); im.ReplyToBot {
			t.Error("ReplyToBot should be false for a non-quoting post")
		}
	})

	t.Run("channel post carries ConversationDisplay (channel name)", func(t *testing.T) {
		ev := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "p6", UserID: "user2", ChannelID: "channamed", Message: "hi"},
			ChannelType: "O",
		}
		if im := b.incomingFromMattermost(client, ev); im.ConversationDisplay != "Release Team" {
			t.Errorf("ConversationDisplay = %q, want \"Release Team\"", im.ConversationDisplay)
		}
	})

	t.Run("DM carries empty ConversationDisplay", func(t *testing.T) {
		ev := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "p7", UserID: "user1", ChannelID: "chan1", Message: "hi"},
			ChannelType: "D",
		}
		if im := b.incomingFromMattermost(client, ev); im.ConversationDisplay != "" {
			t.Errorf("ConversationDisplay = %q, want empty for DM", im.ConversationDisplay)
		}
	})

	t.Run("Mention set when bot id is among mentions", func(t *testing.T) {
		mentioned := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "p4", UserID: "user2", ChannelID: "chan2", Message: "@laplaced ?"},
			ChannelType: "O",
			Mentions:    []string{"someone", "botid"},
		}
		if im := b.incomingFromMattermost(client, mentioned); !im.Mention {
			t.Error("Mention should be true when botid is in mentions")
		}

		notMentioned := mattermost.PostedEvent{
			Post:        mattermost.Post{ID: "p5", UserID: "user2", ChannelID: "chan2", Message: "chatter"},
			ChannelType: "O",
			Mentions:    []string{"someone"},
		}
		if im := b.incomingFromMattermost(client, notMentioned); im.Mention {
			t.Error("Mention should be false when botid is absent from mentions")
		}
	})
}

func TestMMShouldProcess(t *testing.T) {
	tests := []struct {
		name  string
		post  mattermost.Post
		botID string
		want  bool
	}{
		// Own/system posts are dropped; everything else passes through (the access
		// gate now lives in HandleIncoming, not here).
		{"own post ignored", mattermost.Post{UserID: "bot", Type: ""}, "bot", false},
		{"system post ignored", mattermost.Post{UserID: "u1", Type: "system_join_channel"}, "bot", false},
		{"DM real-user post admitted", mattermost.Post{UserID: "u1", Type: ""}, "bot", true},
		{"channel real-user post admitted", mattermost.Post{UserID: "u1", Type: ""}, "bot", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mmShouldProcess(tt.post, tt.botID); got != tt.want {
				t.Errorf("mmShouldProcess = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMMIdempotencyKey_StableAndDistinct(t *testing.T) {
	a := mmIdempotencyKey("chan", "root", "text")
	b := mmIdempotencyKey("chan", "root", "text")
	if a != b {
		t.Error("idempotency key must be stable for the same tuple")
	}
	if a == mmIdempotencyKey("chan", "root", "different") {
		t.Error("idempotency key must differ for different text")
	}
}
