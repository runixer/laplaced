package ui

import (
	"github.com/runixer/laplaced/internal/storage"
)

// PageData holds the common data required for rendering pages.
type PageData struct {
	Users          []storage.User
	SelectedUserID int64
	Data           interface{}
}
