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
	"github.com/runixer/laplaced/internal/testutil"
)

func TestMMRenderer_PassThroughAndSplit(t *testing.T) {
	r := NewMattermostRenderer(16383, testutil.TestLogger())

	t.Run("markdown passes through unescaped", func(t *testing.T) {
		chunks, err := r.Render("**bold** and `code` and <not html>")
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
		chunks, err := r.Render("part one###SPLIT###part two")
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(chunks) != 2 {
			t.Fatalf("want 2 chunks, got %d: %v", len(chunks), chunks)
		}
	})

	t.Run("blank chunks dropped", func(t *testing.T) {
		chunks, _ := r.Render("   ")
		if len(chunks) != 0 {
			t.Errorf("want 0 chunks for blank input, got %d", len(chunks))
		}
	})
}

func TestMMRenderer_RespectsMaxLen(t *testing.T) {
	r := NewMattermostRenderer(300, testutil.TestLogger()) // tiny limit
	long := strings.Repeat("word ", 200)                   // ~1000 chars
	chunks, err := r.Render(long)
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

func TestMMTransport_Kind(t *testing.T) {
	tr := &MMTransport{}
	if tr.Kind() != transportTime {
		t.Errorf("Kind() = %q, want %q", tr.Kind(), transportTime)
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
}

func TestMMShouldProcess(t *testing.T) {
	allowAll := func(string) bool { return true }
	allowNone := func(string) bool { return false }

	tests := []struct {
		name    string
		post    mattermost.Post
		botID   string
		allowed func(string) bool
		want    bool
	}{
		{"normal allowed user post", mattermost.Post{UserID: "u1", Type: ""}, "bot", allowAll, true},
		{"own post ignored", mattermost.Post{UserID: "bot", Type: ""}, "bot", allowAll, false},
		{"system post ignored", mattermost.Post{UserID: "u1", Type: "system_join_channel"}, "bot", allowAll, false},
		{"non-allowed user rejected", mattermost.Post{UserID: "u1", Type: ""}, "bot", allowNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mmShouldProcess(tt.post, tt.botID, tt.allowed); got != tt.want {
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
