package mattermost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestClient spins up an httptest server with the given handler and returns a
// client pointed at it (bootstrap calls hit the same handler).
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(context.Background(), Config{ServerURL: srv.URL, BotToken: "tok123"}, testLogger())
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	return c, srv
}

// bootstrapHandler answers the /users/me and /config/client bootstrap calls.
func bootstrapHandler(maxPostSize string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			_ = json.NewEncoder(w).Encode(User{ID: "botid26charxxxxxxxxxxxxxxx", Username: "laplaced", IsBot: true})
		case "/api/v4/config/client":
			_ = json.NewEncoder(w).Encode(map[string]string{"MaxPostSize": maxPostSize})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}
}

func TestNewClient_BootstrapsIdentityAndMaxPostSize(t *testing.T) {
	c, _ := newTestClient(t, bootstrapHandler("16383"))
	if c.BotID() != "botid26charxxxxxxxxxxxxxxx" {
		t.Errorf("BotID = %q, want bot id from /users/me", c.BotID())
	}
	if c.MaxPostSize() != 16383 {
		t.Errorf("MaxPostSize = %d, want 16383", c.MaxPostSize())
	}
}

func TestNewClient_MaxPostSizeFallback(t *testing.T) {
	// config/client returns no usable MaxPostSize → fall back to the safe default.
	c, _ := newTestClient(t, bootstrapHandler(""))
	if c.MaxPostSize() != defaultMaxPostSize {
		t.Errorf("MaxPostSize = %d, want fallback %d", c.MaxPostSize(), defaultMaxPostSize)
	}
}

func TestNewClient_RequiresServerURL(t *testing.T) {
	_, err := NewClient(context.Background(), Config{BotToken: "x"}, testLogger())
	if err == nil {
		t.Fatal("expected error for empty server_url")
	}
}

func TestCreatePost_BodyAndAuth(t *testing.T) {
	var gotBody createPostReq
	var gotAuth string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me", "/api/v4/config/client":
			bootstrapHandler("16383")(w, r)
		case "/api/v4/posts":
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(Post{ID: "newpostid", ChannelID: gotBody.ChannelID})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})

	post, err := c.CreatePost(context.Background(), CreatePostReq{
		ChannelID: "chan1", Message: "hello", RootID: "root9", IdempotencyKey: "idem1",
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if post.ID != "newpostid" {
		t.Errorf("returned post id = %q, want newpostid", post.ID)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want Bearer tok123", gotAuth)
	}
	if gotBody.ChannelID != "chan1" || gotBody.Message != "hello" || gotBody.RootID != "root9" || gotBody.IdempotencyKey != "idem1" {
		t.Errorf("post body = %+v, missing/incorrect fields", gotBody)
	}
}

func TestSetReaction_Body(t *testing.T) {
	var got map[string]string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me", "/api/v4/config/client":
			bootstrapHandler("16383")(w, r)
		case "/api/v4/reactions":
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})

	if err := c.SetReaction(context.Background(), "post7", "eyes"); err != nil {
		t.Fatalf("SetReaction: %v", err)
	}
	if got["user_id"] != c.BotID() || got["post_id"] != "post7" || got["emoji_name"] != "eyes" {
		t.Errorf("reaction body = %+v", got)
	}
}

func TestSendTyping_Path(t *testing.T) {
	var gotPath string
	var got map[string]string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/users/me" || r.URL.Path == "/api/v4/config/client":
			bootstrapHandler("16383")(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v4/users/") && strings.HasSuffix(r.URL.Path, "/typing"):
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	})

	if err := c.SendTyping(context.Background(), "chanA"); err != nil {
		t.Fatalf("SendTyping: %v", err)
	}
	if gotPath != "/api/v4/users/"+c.BotID()+"/typing" {
		t.Errorf("typing endpoint path = %q, want /users/{botID}/typing", gotPath)
	}
	if got["channel_id"] != "chanA" {
		t.Errorf("typing body channel_id = %q, want chanA", got["channel_id"])
	}
}

func TestDo_SurfacesAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me", "/api/v4/config/client":
			bootstrapHandler("16383")(w, r)
		default:
			http.Error(w, `{"message":"boom"}`, http.StatusBadRequest)
		}
	})
	_, err := c.CreatePost(context.Background(), CreatePostReq{ChannelID: "c", Message: "m"})
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
}
