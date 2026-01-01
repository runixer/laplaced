package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/storage"
)

func TestFindChunkBounds(t *testing.T) {
	tests := []struct {
		name      string
		chunk     []storage.Message
		wantMinID int64
		wantMaxID int64
	}{
		{
			name:      "empty chunk",
			chunk:     []storage.Message{},
			wantMinID: 0,
			wantMaxID: 0,
		},
		{
			name:      "single message",
			chunk:     []storage.Message{{ID: 5}},
			wantMinID: 5,
			wantMaxID: 5,
		},
		{
			name:      "ordered messages",
			chunk:     []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}},
			wantMinID: 1,
			wantMaxID: 3,
		},
		{
			name:      "unordered messages",
			chunk:     []storage.Message{{ID: 5}, {ID: 2}, {ID: 8}, {ID: 1}},
			wantMinID: 1,
			wantMaxID: 8,
		},
		{
			name:      "messages with gaps",
			chunk:     []storage.Message{{ID: 10}, {ID: 20}, {ID: 15}},
			wantMinID: 10,
			wantMaxID: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minID, maxID := findChunkBounds(tt.chunk)
			assert.Equal(t, tt.wantMinID, minID)
			assert.Equal(t, tt.wantMaxID, maxID)
		})
	}
}

func TestFindStragglers(t *testing.T) {
	tests := []struct {
		name               string
		chunk              []storage.Message
		topics             []ExtractedTopic
		wantStragglers     []int64
		wantStragglerCount int
	}{
		{
			name:           "empty chunk",
			chunk:          []storage.Message{},
			topics:         []ExtractedTopic{{StartMsgID: 1, EndMsgID: 5}},
			wantStragglers: nil,
		},
		{
			name:               "empty topics - all stragglers",
			chunk:              []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}},
			topics:             []ExtractedTopic{},
			wantStragglerCount: 3,
		},
		{
			name:  "all covered by single topic",
			chunk: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}},
			topics: []ExtractedTopic{
				{StartMsgID: 1, EndMsgID: 3},
			},
			wantStragglers: nil,
		},
		{
			name:  "partial coverage",
			chunk: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}},
			topics: []ExtractedTopic{
				{StartMsgID: 1, EndMsgID: 2},
			},
			wantStragglerCount: 3, // 3, 4, 5
		},
		{
			name:  "multiple topics with gap",
			chunk: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}},
			topics: []ExtractedTopic{
				{StartMsgID: 1, EndMsgID: 2},
				{StartMsgID: 4, EndMsgID: 5},
			},
			wantStragglerCount: 1, // only 3
		},
		{
			name:  "overlapping topics",
			chunk: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}},
			topics: []ExtractedTopic{
				{StartMsgID: 1, EndMsgID: 3},
				{StartMsgID: 2, EndMsgID: 4},
			},
			wantStragglers: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findStragglers(tt.chunk, tt.topics)

			if tt.wantStragglers != nil {
				assert.Equal(t, tt.wantStragglers, result)
			} else if tt.wantStragglerCount > 0 {
				assert.Len(t, result, tt.wantStragglerCount)
			} else {
				assert.Empty(t, result)
			}
		})
	}
}
