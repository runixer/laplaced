package telegram

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllowedUpdateTypes_ExactSubscription(t *testing.T) {
	assert.Equal(t, []string{"message", "message_reaction"}, AllowedUpdateTypes())
}
