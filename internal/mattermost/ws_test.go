package mattermost

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParsePosted_ReparsesInnerPostString(t *testing.T) {
	c := &Client{logger: testLogger()}

	// The real wire shape: data.post is a JSON *string*, not an object.
	inner := Post{ID: "p1", UserID: "u1", ChannelID: "c1", Message: "hi", Type: ""}
	innerJSON, _ := json.Marshal(inner)
	data, _ := json.Marshal(postedData{Post: string(innerJSON), ChannelType: "D"})

	ev, ok := c.parsePosted(data)
	if !ok {
		t.Fatal("parsePosted returned ok=false for valid event")
	}
	if ev.Post.ID != "p1" || ev.Post.Message != "hi" || ev.ChannelType != "D" {
		t.Errorf("parsed event = %+v, want id p1 / msg hi / type D", ev)
	}
}

func TestParsePosted_BadInnerJSON(t *testing.T) {
	c := &Client{logger: testLogger()}
	data, _ := json.Marshal(postedData{Post: "{not json", ChannelType: "D"})
	if _, ok := c.parsePosted(data); ok {
		t.Error("expected ok=false for malformed inner post json")
	}
}

func TestParsePosted_Mentions(t *testing.T) {
	c := &Client{logger: testLogger()}
	inner := Post{ID: "p1", UserID: "u1", ChannelID: "c1", Message: "@bot hi", Type: ""}
	innerJSON, _ := json.Marshal(inner)

	// MM canon: data.mentions is a JSON-encoded *string* holding a user-id array.
	data, _ := json.Marshal(postedData{
		Post:        string(innerJSON),
		ChannelType: "O",
		Mentions:    `["botid26char","u2"]`,
	})
	ev, ok := c.parsePosted(data)
	if !ok {
		t.Fatal("parsePosted returned ok=false")
	}
	if len(ev.Mentions) != 2 || ev.Mentions[0] != "botid26char" {
		t.Errorf("mentions = %v, want [botid26char u2]", ev.Mentions)
	}
}

func TestParsePosted_NoMentions(t *testing.T) {
	c := &Client{logger: testLogger()}
	inner := Post{ID: "p1", UserID: "u1", ChannelID: "c1", Message: "plain", Type: ""}
	innerJSON, _ := json.Marshal(inner)

	// Absent mentions field (the common channel-chatter case) → nil, ok=true.
	data, _ := json.Marshal(postedData{Post: string(innerJSON), ChannelType: "O"})
	ev, ok := c.parsePosted(data)
	if !ok {
		t.Fatal("parsePosted returned ok=false")
	}
	if len(ev.Mentions) != 0 {
		t.Errorf("mentions = %v, want empty", ev.Mentions)
	}
}

func TestWSURLFromServer(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://time.example.com", "wss://time.example.com/api/v4/websocket"},
		{"http://localhost:8065", "ws://localhost:8065/api/v4/websocket"},
	}
	for _, tt := range tests {
		if got := wsURLFromServer(tt.in); got != tt.want {
			t.Errorf("wsURLFromServer(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNextBackoff_DoublesAndCaps(t *testing.T) {
	// Doubles from 1s.
	if b := nextBackoff(1 * time.Second); b < 2*time.Second {
		t.Errorf("nextBackoff(1s) = %v, want >= 2s", b)
	}
	// Caps near the max (base capped at wsMaxBackoff, plus <=20%% jitter).
	if b := nextBackoff(wsMaxBackoff); b > wsMaxBackoff+wsMaxBackoff/5+time.Second {
		t.Errorf("nextBackoff(max) = %v, exceeds cap+jitter", b)
	}
}
