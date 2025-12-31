package bot

import (
	"encoding/json"
	"strconv"
)

// ReactionType represents a reaction type.
type ReactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji,omitempty"`
}

// SetMessageReactionConfig contains information about a reaction to be set on a message.
type SetMessageReactionConfig struct {
	ChatID    int64
	MessageID int
	Reaction  []ReactionType
	IsBig     bool
}

// Params returns a map of parameters for the SetMessageReaction method.
func (config SetMessageReactionConfig) Params() (map[string]string, error) {
	params := make(map[string]string)
	params["chat_id"] = strconv.FormatInt(config.ChatID, 10)
	params["message_id"] = strconv.Itoa(config.MessageID)

	if len(config.Reaction) > 0 {
		reactionJSON, err := json.Marshal(config.Reaction)
		if err != nil {
			return nil, err
		}
		params["reaction"] = string(reactionJSON)
	}

	if config.IsBig {
		params["is_big"] = "true"
	}

	return params, nil
}

// Method returns the method name for the SetMessageReaction method.
func (config SetMessageReactionConfig) Method() string {
	return "setMessageReaction"
}
