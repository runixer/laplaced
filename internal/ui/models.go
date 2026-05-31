package ui

import (
	"github.com/runixer/laplaced/internal/storage"
)

// PageData is the common data passed to all templates.
type PageData struct {
	Users          []storage.User
	SelectedUserID int64
	ChannelScopes  map[int64]bool // user_id -> scope is a channel (Time O/P/G); for selector labeling
	Data           interface{}
}
