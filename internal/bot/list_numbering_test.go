package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFixListNumbering(t *testing.T) {
	tests := []struct {
		name     string
		parts    []string
		expected []string
	}{
		{
			name:     "single part - no change",
			parts:    []string{"1. first\n2. second"},
			expected: []string{"1. first\n2. second"},
		},
		{
			name:     "no numbered lists - no change",
			parts:    []string{"Hello world", "How are you?"},
			expected: []string{"Hello world", "How are you?"},
		},
		{
			name: "broken numbering - fix 1 to 6",
			parts: []string{
				"4. @user4\n5. @user5",
				"1. @user6\n7. @user7\n8. @user8",
			},
			expected: []string{
				"4. @user4\n5. @user5",
				"6. @user6\n7. @user7\n8. @user8",
			},
		},
		{
			name: "intentional new list - no change",
			parts: []string{
				"5. item five",
				"1. new list item one\n2. new list item two",
			},
			expected: []string{
				"5. item five",
				"1. new list item one\n2. new list item two",
			},
		},
		{
			name: "correct continuation - no change",
			parts: []string{
				"1. first\n2. second",
				"3. third\n4. fourth",
			},
			expected: []string{
				"1. first\n2. second",
				"3. third\n4. fourth",
			},
		},
		{
			name: "three parts with break in second",
			parts: []string{
				"1. one\n2. two",
				"1. should be three\n4. four",
				"5. five",
			},
			expected: []string{
				"1. one\n2. two",
				"3. should be three\n4. four",
				"5. five",
			},
		},
		{
			name: "text before list - still fixes",
			parts: []string{
				"Some intro text\n1. first\n2. second",
				"More text\n1. should be third\n4. fourth",
			},
			expected: []string{
				"Some intro text\n1. first\n2. second",
				"More text\n3. should be third\n4. fourth",
			},
		},
		{
			name: "single item in second part - fixes anyway",
			parts: []string{
				"10. item ten",
				"1. should be eleven",
			},
			expected: []string{
				"10. item ten",
				"11. should be eleven",
			},
		},
		{
			name: "real world LLM glitch - telegram handles list",
			parts: []string{
				"Вот список каналов:\n\n🇷🇺 Россия\n4. @channel_ru — «Новости»\n\n🇬🇧 UK\n5. @channel_uk — один из лучших каналов",
				"1. @channel_de — русскоязычный канал\n\n❄️ Скандинавия\n7. @channel_no — планы на зиму\n8. @channel_se — искать тут",
			},
			expected: []string{
				"Вот список каналов:\n\n🇷🇺 Россия\n4. @channel_ru — «Новости»\n\n🇬🇧 UK\n5. @channel_uk — один из лучших каналов",
				"6. @channel_de — русскоязычный канал\n\n❄️ Скандинавия\n7. @channel_no — планы на зиму\n8. @channel_se — искать тут",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fixListNumbering(tt.parts)
			assert.Equal(t, tt.expected, result)
		})
	}
}
