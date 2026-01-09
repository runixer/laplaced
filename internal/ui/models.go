package ui

import (
	"github.com/runixer/laplaced/internal/storage"
)

// PageData is the common data passed to all templates.
type PageData struct {
	Users          []storage.User
	SelectedUserID int64
	Data           interface{}
}
