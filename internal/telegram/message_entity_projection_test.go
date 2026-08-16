package telegram

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectMessageEntitiesSupportsEveryOfficialType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		entity  MessageEntity
		want    string
		partial bool
	}{
		{name: "mention", text: "@alice", entity: MessageEntity{Type: MessageEntityTypeMention}, want: "@alice"},
		{name: "hashtag", text: "#topic", entity: MessageEntity{Type: MessageEntityTypeHashtag}, want: "#topic"},
		{name: "cashtag", text: "$USD", entity: MessageEntity{Type: MessageEntityTypeCashtag}, want: "$USD"},
		{name: "bot command", text: "/start", entity: MessageEntity{Type: MessageEntityTypeBotCommand}, want: "/start"},
		{name: "url", text: "https://t.me", entity: MessageEntity{Type: MessageEntityTypeURL}, want: "https://t.me"},
		{name: "email", text: "a@b.test", entity: MessageEntity{Type: MessageEntityTypeEmail}, want: "a@b.test"},
		{name: "phone", text: "+1202", entity: MessageEntity{Type: MessageEntityTypePhoneNumber}, want: "+1202"},
		{name: "bold", text: "value", entity: MessageEntity{Type: MessageEntityTypeBold}, want: "**value**"},
		{name: "italic", text: "value", entity: MessageEntity{Type: MessageEntityTypeItalic}, want: "*value*"},
		{name: "underline", text: "value", entity: MessageEntity{Type: MessageEntityTypeUnderline}, want: "value"},
		{name: "strikethrough", text: "value", entity: MessageEntity{Type: MessageEntityTypeStrikethrough}, want: "~~value~~"},
		{name: "spoiler", text: "value", entity: MessageEntity{Type: MessageEntityTypeSpoiler}, want: "||value||"},
		{name: "blockquote", text: "value", entity: MessageEntity{Type: MessageEntityTypeBlockquote}, want: "> value"},
		{name: "expandable blockquote", text: "value", entity: MessageEntity{Type: MessageEntityTypeExpandableBlockquote}, want: "> value"},
		{name: "code", text: "value", entity: MessageEntity{Type: MessageEntityTypeCode}, want: "` value `"},
		{name: "pre", text: "value", entity: MessageEntity{Type: MessageEntityTypePre, Language: "go"}, want: "```go\nvalue\n```"},
		{name: "text link", text: "label", entity: MessageEntity{Type: MessageEntityTypeTextLink, URL: "https://example.com"}, want: "label (URL: https://example\\.com)"},
		{name: "text mention", text: "Alice", entity: MessageEntity{Type: MessageEntityTypeTextMention, User: &User{ID: 42}}, want: "Alice [Telegram user id=42]"},
		{name: "custom emoji", text: "🙂", entity: MessageEntity{Type: MessageEntityTypeCustomEmoji, CustomEmojiID: "emoji-1"}, want: "🙂"},
		{name: "date time", text: "tomorrow", entity: MessageEntity{Type: MessageEntityTypeDateTime, UnixTime: 1_700_000_000, DateTimeFormat: "wDT"}, want: "tomorrow [time: unix=1700000000, format=wDT]"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.entity.Offset = 0
			test.entity.Length = utf16Length(test.text)
			got, err := ProjectMessageEntities(test.text, []MessageEntity{test.entity})
			require.NoError(t, err)
			assert.Equal(t, test.want, got.Markdown)
			assert.Equal(t, test.partial, got.Partial)
			assert.Equal(t, 1, got.EntityCount)
		})
	}
}

func TestProjectMessageEntitiesCodePreservesDollarTextExactly(t *testing.T) {
	t.Parallel()

	const source = "$x_i$"
	got, err := ProjectMessageEntities(source, []MessageEntity{{
		Type:   MessageEntityTypeCode,
		Offset: 0,
		Length: utf16Length(source),
	}})
	require.NoError(t, err)
	assert.Equal(t, "` $x_i$ `", got.Markdown)
	assert.NotContains(t, got.Markdown, `\$`)
	assert.False(t, got.Partial)
}

func TestProjectMessageEntitiesUsesUTF16Boundaries(t *testing.T) {
	t.Parallel()

	const source = "🙂 bold"
	got, err := ProjectMessageEntities(source, []MessageEntity{{
		Type:   MessageEntityTypeBold,
		Offset: 3, // 🙂 is two UTF-16 code units, followed by one space.
		Length: 4,
	}})
	require.NoError(t, err)
	assert.Equal(t, "🙂 **bold**", got.Markdown)
}

func TestProjectMessageEntitiesNestedStylesAndLink(t *testing.T) {
	t.Parallel()

	const source = "linked"
	got, err := ProjectMessageEntities(source, []MessageEntity{
		{Type: MessageEntityTypeBold, Offset: 0, Length: 6},
		{Type: MessageEntityTypeItalic, Offset: 1, Length: 4},
		{Type: MessageEntityTypeTextLink, Offset: 0, Length: 6, URL: "https://example.com/a"},
	})
	require.NoError(t, err)
	assert.Equal(t, "**l*inke*d** (URL: https://example\\.com/a)", got.Markdown)
	assert.Equal(t, 3, got.EntityCount)
	assert.False(t, got.Partial)
}

func TestProjectMessageEntitiesPreAndQuote(t *testing.T) {
	t.Parallel()

	const pre = "fmt.Println(`x`)"
	const quote = "quote\nline"
	source := pre + "\n" + quote
	got, err := ProjectMessageEntities(source, []MessageEntity{
		{Type: MessageEntityTypePre, Offset: 0, Length: utf16Length(pre), Language: "go"},
		{Type: MessageEntityTypeBlockquote, Offset: utf16Length(pre) + 1, Length: utf16Length(quote)},
	})
	require.NoError(t, err)
	assert.Equal(t, "```go\nfmt.Println(`x`)\n```\n> quote\n> line", got.Markdown)
}

func TestProjectMessageEntitiesUnknownAndUnsafeLinkArePlain(t *testing.T) {
	t.Parallel()

	const source = "mystery click"
	got, err := ProjectMessageEntities(source, []MessageEntity{
		{Type: "future_entity", Offset: 0, Length: 7},
		{Type: MessageEntityTypeTextLink, Offset: 8, Length: 5, URL: "javascript:alert(1)"},
	})
	require.NoError(t, err)
	assert.Equal(t, "mystery click (URL: javascript:alert\\(1\\))", got.Markdown)
	assert.NotContains(t, got.Markdown, "](javascript:")
	assert.True(t, got.Partial)
	assert.Equal(t, 2, got.EntityCount)
}

func TestProjectMessageEntitiesRejectsInvalidRangesAtomically(t *testing.T) {
	t.Parallel()

	maxInteger := int(^uint(0) >> 1)
	tests := []struct {
		name     string
		text     string
		entities []MessageEntity
		field    string
	}{
		{
			name: "surrogate split",
			text: "🙂x",
			entities: []MessageEntity{{
				Type: MessageEntityTypeBold, Offset: 1, Length: 1,
			}},
			field: "range",
		},
		{
			name: "crossing",
			text: "abcdef",
			entities: []MessageEntity{
				{Type: MessageEntityTypeBold, Offset: 0, Length: 4},
				{Type: MessageEntityTypeItalic, Offset: 2, Length: 4},
			},
			field: "overlap",
		},
		{
			name: "non-style nesting",
			text: "abcdef",
			entities: []MessageEntity{
				{Type: MessageEntityTypeTextLink, Offset: 0, Length: 6, URL: "https://example.com"},
				{Type: MessageEntityTypeMention, Offset: 1, Length: 4},
			},
			field: "nesting",
		},
		{
			name: "code overlap",
			text: "abcdef",
			entities: []MessageEntity{
				{Type: MessageEntityTypeCode, Offset: 0, Length: 6},
				{Type: MessageEntityTypeBold, Offset: 1, Length: 4},
			},
			field: "nesting",
		},
		{
			name: "integer overflow",
			text: "x",
			entities: []MessageEntity{{
				Type: MessageEntityTypeBold, Offset: maxInteger, Length: 1,
			}},
			field: "range",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ProjectMessageEntities(test.text, test.entities)
			require.Error(t, err)
			assert.Equal(t, MessageEntityProjection{}, got, "error must never expose a partially rendered projection")
			var projectionErr *MessageEntityProjectionError
			require.True(t, errors.As(err, &projectionErr))
			assert.Equal(t, test.field, projectionErr.Field)
		})
	}
}

func TestProjectMessageEntitiesBoundsEntityCountAndSource(t *testing.T) {
	t.Parallel()

	tooMany := make([]MessageEntity, MessageEntityProjectionMaxEntities+1)
	got, err := ProjectMessageEntities("x", tooMany)
	require.Error(t, err)
	assert.Equal(t, MessageEntityProjection{}, got)

	source := strings.Repeat("*", MessageEntityProjectionMaxSourceCharacters+1)
	got, err = ProjectMessageEntities(source, []MessageEntity{{
		Type: "future_entity", Offset: 0, Length: utf16Length(source),
	}})
	require.Error(t, err)
	assert.Equal(t, MessageEntityProjection{}, got)
}

func TestProjectMessageEntitiesBoundsMetadataBeforeRender(t *testing.T) {
	t.Parallel()

	const source = "x"
	typeBytes := len(MessageEntityTypeTextLink)
	withinLimit := strings.Repeat("a", MessageEntityProjectionMaxMetadataBytes-typeBytes)
	got, err := ProjectMessageEntities(source, []MessageEntity{{
		Type: MessageEntityTypeTextLink, Offset: 0, Length: 1, URL: withinLimit,
	}})
	require.NoError(t, err)
	assert.NotEmpty(t, got.Markdown)

	got, err = ProjectMessageEntities(source, []MessageEntity{{
		Type: MessageEntityTypeTextLink, Offset: 0, Length: 1, URL: withinLimit + "a",
	}})
	require.Error(t, err)
	assert.Equal(t, MessageEntityProjection{}, got)
	var projectionErr *MessageEntityProjectionError
	require.ErrorAs(t, err, &projectionErr)
	assert.Equal(t, "metadata_bytes", projectionErr.Field)
	assert.Equal(t, MessageEntityProjectionMaxMetadataBytes, projectionErr.Limit)
}

func TestProjectMessageEntitiesBoundsNestingDepth(t *testing.T) {
	t.Parallel()

	makeNested := func(depth int) (string, []MessageEntity) {
		text := strings.Repeat("x", depth*2-1)
		entities := make([]MessageEntity, depth)
		for i := range entities {
			entities[i] = MessageEntity{
				Type: MessageEntityTypeBold, Offset: i, Length: len(text) - 2*i,
			}
		}
		return text, entities
	}

	text, entities := makeNested(MessageEntityProjectionMaxNestingDepth)
	_, err := ProjectMessageEntities(text, entities)
	require.NoError(t, err)

	text, entities = makeNested(MessageEntityProjectionMaxNestingDepth + 1)
	got, err := ProjectMessageEntities(text, entities)
	require.Error(t, err)
	assert.Equal(t, MessageEntityProjection{}, got)
	var projectionErr *MessageEntityProjectionError
	require.ErrorAs(t, err, &projectionErr)
	assert.Equal(t, "nesting_depth", projectionErr.Field)
	assert.Equal(t, MessageEntityProjectionMaxNestingDepth+1, projectionErr.Actual)
}

func TestProjectMessageEntitiesMakesInlineBlocksStructurallySafe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		text   string
		entity MessageEntity
		want   string
	}{
		{
			name: "inline quote",
			text: "before quoted after",
			entity: MessageEntity{
				Type: MessageEntityTypeBlockquote, Offset: 7, Length: 6,
			},
			want: "before \n> quoted\n after",
		},
		{
			name: "inline pre",
			text: "before code after",
			entity: MessageEntity{
				Type: MessageEntityTypePre, Offset: 7, Length: 4,
			},
			want: "before \n```\ncode\n```\n after",
		},
		{
			name: "already line aligned",
			text: "before\nquoted\nafter",
			entity: MessageEntity{
				Type: MessageEntityTypeBlockquote, Offset: 7, Length: 6,
			},
			want: "before\n> quoted\nafter",
		},
		{
			name: "crlf aligned",
			text: "before\r\nquoted\r\nafter",
			entity: MessageEntity{
				Type: MessageEntityTypeBlockquote, Offset: 8, Length: 6,
			},
			want: "before\r\n> quoted\r\nafter",
		},
		{
			name: "lone carriage return inside quote",
			text: "first\rsecond",
			entity: MessageEntity{
				Type: MessageEntityTypeBlockquote, Offset: 0, Length: 12,
			},
			want: "> first\r> second",
		},
		{
			name: "edge whitespace preserved",
			text: "  quote  ",
			entity: MessageEntity{
				Type: MessageEntityTypeBlockquote, Offset: 0, Length: 9,
			},
			want: ">   quote  ",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ProjectMessageEntities(test.text, []MessageEntity{test.entity})
			require.NoError(t, err)
			assert.Equal(t, test.want, got.Markdown)
		})
	}
}

func TestProjectMessageEntitiesBlockNestingPolicy(t *testing.T) {
	t.Parallel()

	const source = "quoted"
	got, err := ProjectMessageEntities(source, []MessageEntity{
		{Type: MessageEntityTypeBlockquote, Offset: 0, Length: 6},
		{Type: MessageEntityTypeBold, Offset: 0, Length: 6},
	})
	require.NoError(t, err)
	assert.Equal(t, "> **quoted**", got.Markdown)

	got, err = ProjectMessageEntities("aquotedb", []MessageEntity{
		{Type: MessageEntityTypeBold, Offset: 0, Length: 8},
		{Type: MessageEntityTypeBlockquote, Offset: 1, Length: 6},
	})
	require.Error(t, err)
	assert.Equal(t, MessageEntityProjection{}, got)
}

func TestProjectMessageEntitiesMarksSanitizedMetadataPartial(t *testing.T) {
	t.Parallel()

	got, err := ProjectMessageEntities("link", []MessageEntity{{
		Type: MessageEntityTypeTextLink, Offset: 0, Length: 4, URL: "https://example.test/\nunsafe",
	}})
	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.NotContains(t, got.Markdown, "\nunsafe")

	got, err = ProjectMessageEntities("code", []MessageEntity{{
		Type: MessageEntityTypePre, Offset: 0, Length: 4, Language: "go unsafe",
	}})
	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.Equal(t, "```\ncode\n```", got.Markdown)
}

func TestMessageDecodesTextAndCaptionEntities(t *testing.T) {
	t.Parallel()

	const payload = `{
		"message_id":7,
		"chat":{"id":42,"type":"private"},
		"date":1700000000,
		"text":"hello",
		"entities":[{"type":"text_mention","offset":0,"length":5,"user":{"id":99,"is_bot":false,"first_name":"Alice"}}],
		"caption":"code",
		"caption_entities":[{"type":"pre","offset":0,"length":4,"language":"go"}]
	}`
	var message Message
	require.NoError(t, json.Unmarshal([]byte(payload), &message))
	require.Len(t, message.Entities, 1)
	assert.Equal(t, MessageEntityTypeTextMention, message.Entities[0].Type)
	require.NotNil(t, message.Entities[0].User)
	assert.EqualValues(t, 99, message.Entities[0].User.ID)
	require.Len(t, message.CaptionEntities, 1)
	assert.Equal(t, MessageEntityTypePre, message.CaptionEntities[0].Type)
	assert.Equal(t, "go", message.CaptionEntities[0].Language)
}

func TestProjectMessageEntitiesPreservesPlainLeaves(t *testing.T) {
	t.Parallel()

	got, err := ProjectMessageEntities("$x_i$", []MessageEntity{{
		Type: "future_entity", Offset: 0, Length: 5,
	}})
	require.NoError(t, err)
	assert.Equal(t, `$x_i$`, got.Markdown)
	assert.Equal(t, 1, got.EntityCount)
	assert.True(t, got.Partial)
}

func TestProjectMessageEntitiesPreservesLegacyMarkdownOutsideCode(t *testing.T) {
	t.Parallel()

	const source = "Формула $x^2+y^2=r^2$; цена $100, диапазон $5–$10; emoji 🙂; код $x_i$; формула $5 x$; ссылка [пример](https://example.com)."
	const code = "$x_i$"
	const rawURL = "https://example.com"
	codeByte := strings.Index(source, code)
	require.NotEqual(t, -1, codeByte)
	urlByte := strings.Index(source, rawURL)
	require.NotEqual(t, -1, urlByte)

	got, err := ProjectMessageEntities(source, []MessageEntity{
		{
			Type: MessageEntityTypeCode, Offset: utf16Length(source[:codeByte]), Length: utf16Length(code),
		},
		{
			Type: MessageEntityTypeURL, Offset: utf16Length(source[:urlByte]), Length: utf16Length(rawURL),
		},
	})
	require.NoError(t, err)
	assert.Equal(t,
		"Формула $x^2+y^2=r^2$; цена $100, диапазон $5–$10; emoji 🙂; код ` $x_i$ `; формула $5 x$; ссылка [пример](https://example.com).",
		got.Markdown,
	)
}

func TestProjectMessageEntitiesFlattensConflictingStyleWithoutChangingContent(t *testing.T) {
	t.Parallel()

	const source = `a*b\c $x$`
	got, err := ProjectMessageEntities(source, []MessageEntity{{
		Type: MessageEntityTypeBold, Offset: 0, Length: utf16Length(source),
	}})
	require.NoError(t, err)
	assert.Equal(t, source, got.Markdown)
	assert.True(t, got.Partial)
}

func TestProjectMessageEntitiesPreservesLatexBackslashesInsideStyle(t *testing.T) {
	t.Parallel()

	const source = `Формула $\frac{x^2}{y}$`
	got, err := ProjectMessageEntities(source, []MessageEntity{{
		Type: MessageEntityTypeBold, Offset: 0, Length: utf16Length(source),
	}})
	require.NoError(t, err)
	assert.Equal(t, `**Формула $\frac{x^2}{y}$**`, got.Markdown)
	assert.False(t, got.Partial)
}

func TestProjectMessageEntitiesFlattensAmbiguousStyleShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		entities []MessageEntity
		want     string
	}{
		{
			name: "edge whitespace",
			text: " bold ",
			entities: []MessageEntity{{
				Type: MessageEntityTypeBold, Offset: 0, Length: 6,
			}},
			want: " bold ",
		},
		{
			name: "redundant nested same style",
			text: "abcd",
			entities: []MessageEntity{
				{Type: MessageEntityTypeBold, Offset: 0, Length: 4},
				{Type: MessageEntityTypeBold, Offset: 1, Length: 2},
			},
			want: "a**bc**d",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ProjectMessageEntities(test.text, test.entities)
			require.NoError(t, err)
			assert.Equal(t, test.want, got.Markdown)
			assert.True(t, got.Partial)
		})
	}
}

func TestProjectMessageEntitiesNoEntitiesIsByteIdentical(t *testing.T) {
	t.Parallel()

	const source = "$x_i$ *literal* [text]"
	got, err := ProjectMessageEntities(source, nil)
	require.NoError(t, err)
	assert.Equal(t, source, got.Markdown)
	assert.Zero(t, got.EntityCount)
	assert.False(t, got.Partial)

	large := strings.Repeat("x", MessageEntityProjectionMaxSourceCharacters+1)
	got, err = ProjectMessageEntities(large, nil)
	require.NoError(t, err)
	assert.Equal(t, large, got.Markdown)
}

func TestProjectMessageEntitiesIgnoresEmptyAndDuplicateEntities(t *testing.T) {
	t.Parallel()

	const source = "$x_i$"
	code := MessageEntity{Type: MessageEntityTypeCode, Offset: 0, Length: 5}
	got, err := ProjectMessageEntities(source, []MessageEntity{
		{Type: MessageEntityTypeBold, Offset: -100, Length: 0},
		code,
		code,
	})
	require.NoError(t, err)
	assert.Equal(t, "` $x_i$ `", got.Markdown)
	assert.Equal(t, 1, got.EntityCount)
	assert.False(t, got.Partial)
}

func TestProjectMessageEntitiesUnknownCrossingKnownDoesNotInvalidateKnownFormatting(t *testing.T) {
	t.Parallel()

	const source = "abcdef"
	got, err := ProjectMessageEntities(source, []MessageEntity{
		{Type: MessageEntityTypeBold, Offset: 0, Length: 4},
		{Type: "future_entity", Offset: 2, Length: 4},
	})
	require.NoError(t, err)
	assert.Equal(t, "**abcd**ef", got.Markdown)
	assert.True(t, got.Partial)
	assert.Equal(t, 2, got.EntityCount)
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}
