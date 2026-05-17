package reranker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollectIDs(t *testing.T) {
	tests := []struct {
		name  string
		items []TopicSelection
		want  []int64
	}{
		{
			name:  "empty input returns nil",
			items: nil,
			want:  nil,
		},
		{
			name: "all valid IDs",
			items: []TopicSelection{
				{ID: "Topic:42"},
				{ID: "Topic:7"},
			},
			want: []int64{42, 7},
		},
		{
			name: "bare numeric IDs accepted",
			items: []TopicSelection{
				{ID: "Topic:42"},
				{ID: "7"},
			},
			want: []int64{42, 7},
		},
		{
			name: "malformed IDs skipped, valid ones preserved in order",
			items: []TopicSelection{
				{ID: "Topic:42"},
				{ID: "garbage"},
				{ID: "Topic:7"},
			},
			want: []int64{42, 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectIDs(tt.items, func(s TopicSelection) string { return s.ID }, parseTopicID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDiffIDs(t *testing.T) {
	tests := []struct {
		name string
		raw  []int64
		kept []int64
		want []int64
	}{
		{
			name: "empty raw returns nil",
			raw:  nil,
			kept: []int64{1, 2},
			want: nil,
		},
		{
			name: "nothing hallucinated when all kept",
			raw:  []int64{1, 2, 3},
			kept: []int64{1, 2, 3},
			want: nil,
		},
		{
			name: "everything hallucinated when nothing kept",
			raw:  []int64{99, 100},
			kept: nil,
			want: []int64{99, 100},
		},
		{
			name: "mixed: only the unmatched IDs come back",
			raw:  []int64{1, 999, 2, 888},
			kept: []int64{1, 2},
			want: []int64{999, 888},
		},
		{
			name: "order preserved from raw",
			raw:  []int64{5, 999, 3, 888, 7},
			kept: []int64{3, 5, 7},
			want: []int64{999, 888},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffIDs(tt.raw, tt.kept)
			assert.Equal(t, tt.want, got)
		})
	}
}
