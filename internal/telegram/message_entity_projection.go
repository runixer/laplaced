package telegram

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Internal safety budgets for projecting ordinary Telegram message entities.
// Telegram doesn't publish an entity-count limit, so the count is deliberately
// generous while still bounding overlap validation. The output allowance is
// shared in spirit with rich-message projection and comfortably exceeds the
// maximum ordinary message size.
const (
	MessageEntityProjectionMaxEntities         = 4_096
	MessageEntityProjectionMaxSourceCharacters = 32_768
	MessageEntityProjectionMaxNestingDepth     = RichMessageMaxNestingDepth
	MessageEntityProjectionMaxMetadataBytes    = 64 << 10
	MessageEntityProjectionMaxOutputCharacters = 1_048_576
)

// MessageEntityProjection is the transport-neutral Markdown representation of
// ordinary Telegram text plus MessageEntity ranges. Partial is set when visible
// content was preserved but optional metadata, an unknown future entity type,
// or a delimiter-conflicting presentation wrapper could not be represented.
type MessageEntityProjection struct {
	Markdown    string
	Partial     bool
	EntityCount int
}

// MessageEntityProjectionError describes a content-free validation or safety
// failure. EntityIndex is -1 when the error applies to the whole projection.
// Callers should atomically fall back to their original plain-text path.
type MessageEntityProjectionError struct {
	Field       string
	EntityIndex int
	Reason      string
	Limit       int
	Actual      int
}

func (e *MessageEntityProjectionError) Error() string {
	prefix := "telegram message entity projection"
	if e.EntityIndex >= 0 {
		prefix += fmt.Sprintf(" entity %d", e.EntityIndex)
	}
	if e.Limit > 0 || e.Actual > 0 {
		return fmt.Sprintf("%s %s limit exceeded: %d > %d", prefix, e.Field, e.Actual, e.Limit)
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s invalid %s: %s", prefix, e.Field, e.Reason)
	}
	return fmt.Sprintf("%s invalid %s", prefix, e.Field)
}

// ProjectMessageEntities validates Telegram's UTF-16 ranges and official
// nesting rules, then projects the selected Message.Text or Message.Caption to
// canonical Markdown. It never returns a partial Markdown value alongside an
// error, allowing callers to apply an atomic plain-text fallback.
func ProjectMessageEntities(text string, entities []MessageEntity) (MessageEntityProjection, error) {
	if len(entities) == 0 {
		// Preserve the established ordinary-message path exactly. There are no
		// UTF-16 ranges to apply and therefore no reason to reinterpret or bound
		// the already-decoded source here.
		return MessageEntityProjection{Markdown: text}, nil
	}
	if len(entities) > MessageEntityProjectionMaxEntities {
		return MessageEntityProjection{}, &MessageEntityProjectionError{
			Field:       "entities",
			EntityIndex: -1,
			Limit:       MessageEntityProjectionMaxEntities,
			Actual:      len(entities),
		}
	}
	if !utf8.ValidString(text) {
		return MessageEntityProjection{}, &MessageEntityProjectionError{
			Field:       "text_utf8",
			EntityIndex: -1,
			Reason:      "source text is not valid UTF-8",
		}
	}
	// Every source rune survives the projection, so this is also an allocation
	// guard before building the UTF-16 boundary map.
	if characters := utf8.RuneCountInString(text); characters > MessageEntityProjectionMaxSourceCharacters {
		return MessageEntityProjection{}, &MessageEntityProjectionError{
			Field:       "source_characters",
			EntityIndex: -1,
			Limit:       MessageEntityProjectionMaxSourceCharacters,
			Actual:      characters,
		}
	}

	roots, entityCount, partial, err := prevalidateMessageEntities(text, entities)
	if err != nil {
		return MessageEntityProjection{}, err
	}
	renderer := messageEntityRenderer{text: text, partial: partial}
	markdown := text
	if entityCount > 0 {
		markdown = renderer.renderRange(0, len(text), roots)
	}
	if characters := utf8.RuneCountInString(markdown); characters > MessageEntityProjectionMaxOutputCharacters {
		return MessageEntityProjection{}, &MessageEntityProjectionError{
			Field:       "output_characters",
			EntityIndex: -1,
			Limit:       MessageEntityProjectionMaxOutputCharacters,
			Actual:      characters,
		}
	}
	return MessageEntityProjection{
		Markdown:    markdown,
		Partial:     renderer.partial,
		EntityCount: entityCount,
	}, nil
}

type validatedMessageEntity struct {
	entity MessageEntity
	index  int

	startUTF16 int
	endUTF16   int
	startByte  int
	endByte    int

	children []*validatedMessageEntity
}

func prevalidateMessageEntities(text string, entities []MessageEntity) ([]*validatedMessageEntity, int, bool, error) {
	if len(entities) == 0 {
		return nil, 0, false, nil
	}

	canonical := make([]struct {
		entity MessageEntity
		index  int
		known  bool
	}, 0, len(entities))
	seen := make(map[messageEntityDedupKey]struct{}, len(entities))
	maxInteger := int(^uint(0) >> 1)
	metadataBytes := 0
	for i, entity := range entities {
		// Telegram's MarkdownV2 representation can use an empty bold entity as
		// a separator between adjacent quote blocks. It has no visible semantic
		// content and is safest to ignore regardless of its offset.
		if entity.Length == 0 {
			continue
		}
		if entity.Offset < 0 {
			return nil, 0, false, entityRangeError(i, "offset is negative")
		}
		if entity.Length < 0 {
			return nil, 0, false, entityRangeError(i, "length is negative")
		}
		if entity.Length > maxInteger-entity.Offset {
			return nil, 0, false, entityRangeError(i, "offset plus length overflows int")
		}
		for _, value := range []string{
			string(entity.Type), entity.URL, entity.Language, entity.CustomEmojiID, entity.DateTimeFormat,
		} {
			if !utf8.ValidString(value) {
				return nil, 0, false, &MessageEntityProjectionError{
					Field:       "metadata_utf8",
					EntityIndex: i,
					Reason:      "metadata is not valid UTF-8",
				}
			}
			if len(value) > MessageEntityProjectionMaxMetadataBytes-metadataBytes {
				actual := maxInteger
				if len(value) <= maxInteger-metadataBytes {
					actual = metadataBytes + len(value)
				}
				return nil, 0, false, &MessageEntityProjectionError{
					Field:       "metadata_bytes",
					EntityIndex: i,
					Limit:       MessageEntityProjectionMaxMetadataBytes,
					Actual:      actual,
				}
			}
			metadataBytes += len(value)
		}
		key := makeMessageEntityDedupKey(entity)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		canonical = append(canonical, struct {
			entity MessageEntity
			index  int
			known  bool
		}{entity: entity, index: i, known: isKnownMessageEntityType(entity.Type)})
	}
	if len(canonical) == 0 {
		return nil, 0, false, nil
	}

	wantedBoundaries := make(map[int]struct{}, len(canonical)*2)
	spans := make([]validatedMessageEntity, 0, len(canonical))
	partial := false
	for _, item := range canonical {
		end := item.entity.Offset + item.entity.Length
		if !item.known {
			partial = true
		}
		spans = append(spans, validatedMessageEntity{
			entity:     item.entity,
			index:      item.index,
			startUTF16: item.entity.Offset,
			endUTF16:   end,
		})
		wantedBoundaries[item.entity.Offset] = struct{}{}
		wantedBoundaries[end] = struct{}{}
	}

	byteBoundaries := make(map[int]int, len(wantedBoundaries))
	utf16Offset := 0
	for byteOffset, r := range text {
		if _, wanted := wantedBoundaries[utf16Offset]; wanted {
			byteBoundaries[utf16Offset] = byteOffset
		}
		if r > 0xFFFF {
			utf16Offset += 2
		} else {
			utf16Offset++
		}
	}
	if _, wanted := wantedBoundaries[utf16Offset]; wanted {
		byteBoundaries[utf16Offset] = len(text)
	}

	spanPointers := make([]*validatedMessageEntity, 0, len(spans))
	for i := range spans {
		span := &spans[i]
		startByte, startOK := byteBoundaries[span.startUTF16]
		endByte, endOK := byteBoundaries[span.endUTF16]
		if !startOK || !endOK {
			reason := "range is outside source text"
			if span.startUTF16 <= utf16Offset && span.endUTF16 <= utf16Offset {
				reason = "range boundary splits a UTF-16 surrogate pair"
			}
			return nil, 0, false, entityRangeError(span.index, reason)
		}
		span.startByte = startByte
		span.endByte = endByte
		// Unknown future types have no formatting semantics yet. Excluding them
		// from the interval family keeps valid known formatting usable even if a
		// future entity crosses an older one; its visible text remains in the
		// surrounding plain leaf and Partial records the semantic loss.
		if isKnownMessageEntityType(span.entity.Type) {
			spanPointers = append(spanPointers, span)
		}
	}

	// Known Telegram entities must form a laminar interval family, with stricter
	// containment rules based on type. Validate the complete known family before
	// any caller-visible Markdown is produced.
	for i := 0; i < len(spanPointers); i++ {
		for j := i + 1; j < len(spanPointers); j++ {
			if err := validateMessageEntityPair(spanPointers[i], spanPointers[j]); err != nil {
				return nil, 0, false, err
			}
		}
	}

	sort.SliceStable(spanPointers, func(i, j int) bool {
		a, b := spanPointers[i], spanPointers[j]
		if a.startUTF16 != b.startUTF16 {
			return a.startUTF16 < b.startUTF16
		}
		if a.endUTF16 != b.endUTF16 {
			return a.endUTF16 > b.endUTF16
		}
		if rankA, rankB := messageEntityOuterRank(a.entity.Type), messageEntityOuterRank(b.entity.Type); rankA != rankB {
			return rankA < rankB
		}
		return a.index < b.index
	})

	var roots []*validatedMessageEntity
	stack := make([]*validatedMessageEntity, 0, len(spanPointers))
	for _, span := range spanPointers {
		for len(stack) > 0 && !messageEntityContains(stack[len(stack)-1], span) {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, span)
		} else {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, span)
		}
		stack = append(stack, span)
		if len(stack) > MessageEntityProjectionMaxNestingDepth {
			return nil, 0, false, &MessageEntityProjectionError{
				Field:       "nesting_depth",
				EntityIndex: span.index,
				Limit:       MessageEntityProjectionMaxNestingDepth,
				Actual:      len(stack),
			}
		}
	}
	return roots, len(canonical), partial, nil
}

type messageEntityDedupKey struct {
	Type           MessageEntityType
	Offset         int
	Length         int
	URL            string
	Language       string
	CustomEmojiID  string
	UnixTime       int64
	DateTimeFormat string
	HasUser        bool
	UserID         int64
}

func makeMessageEntityDedupKey(entity MessageEntity) messageEntityDedupKey {
	key := messageEntityDedupKey{
		Type:           entity.Type,
		Offset:         entity.Offset,
		Length:         entity.Length,
		URL:            entity.URL,
		Language:       entity.Language,
		CustomEmojiID:  entity.CustomEmojiID,
		UnixTime:       entity.UnixTime,
		DateTimeFormat: entity.DateTimeFormat,
	}
	if entity.User != nil {
		key.HasUser = true
		key.UserID = entity.User.ID
	}
	return key
}

func entityRangeError(index int, reason string) error {
	return &MessageEntityProjectionError{
		Field:       "range",
		EntityIndex: index,
		Reason:      reason,
	}
}

func validateMessageEntityPair(a, b *validatedMessageEntity) error {
	if a.endUTF16 <= b.startUTF16 || b.endUTF16 <= a.startUTF16 {
		return nil
	}
	aContainsB := messageEntityContains(a, b)
	bContainsA := messageEntityContains(b, a)
	if !aContainsB && !bContainsA {
		return &MessageEntityProjectionError{
			Field:       "overlap",
			EntityIndex: b.index,
			Reason:      fmt.Sprintf("range crosses entity %d", a.index),
		}
	}

	if isMessageEntityCode(a.entity.Type) || isMessageEntityCode(b.entity.Type) {
		return &MessageEntityProjectionError{
			Field:       "nesting",
			EntityIndex: b.index,
			Reason:      fmt.Sprintf("code or pre overlaps entity %d", a.index),
		}
	}
	if (isMessageEntityFlexibleStyle(a.entity.Type) && isMessageEntityBlock(b.entity.Type) && messageEntityStrictlyContains(a, b)) ||
		(isMessageEntityFlexibleStyle(b.entity.Type) && isMessageEntityBlock(a.entity.Type) && messageEntityStrictlyContains(b, a)) {
		return &MessageEntityProjectionError{
			Field:       "nesting",
			EntityIndex: b.index,
			Reason:      fmt.Sprintf("inline style can't contain block entity %d", a.index),
		}
	}
	if isMessageEntityFlexibleStyle(a.entity.Type) || isMessageEntityFlexibleStyle(b.entity.Type) {
		return nil
	}
	return &MessageEntityProjectionError{
		Field:       "nesting",
		EntityIndex: b.index,
		Reason:      fmt.Sprintf("entity type %q can't contain or be contained by entity %d", b.entity.Type, a.index),
	}
}

func messageEntityContains(outer, inner *validatedMessageEntity) bool {
	return outer.startUTF16 <= inner.startUTF16 && outer.endUTF16 >= inner.endUTF16
}

func messageEntityStrictlyContains(outer, inner *validatedMessageEntity) bool {
	return messageEntityContains(outer, inner) &&
		(outer.startUTF16 != inner.startUTF16 || outer.endUTF16 != inner.endUTF16)
}

func isMessageEntityFlexibleStyle(kind MessageEntityType) bool {
	switch kind {
	case MessageEntityTypeBold,
		MessageEntityTypeItalic,
		MessageEntityTypeUnderline,
		MessageEntityTypeStrikethrough,
		MessageEntityTypeSpoiler:
		return true
	default:
		return false
	}
}

func isMessageEntityCode(kind MessageEntityType) bool {
	return kind == MessageEntityTypeCode || kind == MessageEntityTypePre
}

func isMessageEntityBlock(kind MessageEntityType) bool {
	return kind == MessageEntityTypePre ||
		kind == MessageEntityTypeBlockquote ||
		kind == MessageEntityTypeExpandableBlockquote
}

func isKnownMessageEntityType(kind MessageEntityType) bool {
	switch kind {
	case MessageEntityTypeMention,
		MessageEntityTypeHashtag,
		MessageEntityTypeCashtag,
		MessageEntityTypeBotCommand,
		MessageEntityTypeURL,
		MessageEntityTypeEmail,
		MessageEntityTypePhoneNumber,
		MessageEntityTypeBold,
		MessageEntityTypeItalic,
		MessageEntityTypeUnderline,
		MessageEntityTypeStrikethrough,
		MessageEntityTypeSpoiler,
		MessageEntityTypeBlockquote,
		MessageEntityTypeExpandableBlockquote,
		MessageEntityTypeCode,
		MessageEntityTypePre,
		MessageEntityTypeTextLink,
		MessageEntityTypeTextMention,
		MessageEntityTypeCustomEmoji,
		MessageEntityTypeDateTime:
		return true
	default:
		return false
	}
}

func messageEntityOuterRank(kind MessageEntityType) int {
	// Structural/non-style entities should wrap coincident flexible styling, so
	// a link label becomes "**label** (URL: ...)" rather than exposing a target
	// inside formatting delimiters. Remaining ranks make equal style ranges
	// deterministic.
	switch kind {
	case MessageEntityTypeBlockquote, MessageEntityTypeExpandableBlockquote:
		return 0
	case MessageEntityTypeBold:
		return 20
	case MessageEntityTypeItalic:
		return 21
	case MessageEntityTypeUnderline:
		return 22
	case MessageEntityTypeStrikethrough:
		return 23
	case MessageEntityTypeSpoiler:
		return 24
	default:
		return 10
	}
}

type messageEntityRenderer struct {
	text    string
	partial bool
}

func (r *messageEntityRenderer) renderRange(start, end int, children []*validatedMessageEntity) string {
	var out strings.Builder
	cursor := start
	for _, child := range children {
		out.WriteString(r.text[cursor:child.startByte])
		if isMessageEntityBlock(child.entity.Type) && !r.isLineStart(child.startByte) {
			out.WriteByte('\n')
		}
		out.WriteString(r.renderEntity(child))
		if isMessageEntityBlock(child.entity.Type) && !r.isLineEnd(child.endByte) {
			out.WriteByte('\n')
		}
		cursor = child.endByte
	}
	out.WriteString(r.text[cursor:end])
	return out.String()
}

func (r *messageEntityRenderer) isLineStart(offset int) bool {
	return offset == 0 || r.text[offset-1] == '\n' || r.text[offset-1] == '\r'
}

func (r *messageEntityRenderer) isLineEnd(offset int) bool {
	return offset == len(r.text) || r.text[offset] == '\n' || r.text[offset] == '\r'
}

func (r *messageEntityRenderer) renderEntity(span *validatedMessageEntity) string {
	entity := span.entity
	if entity.Type == MessageEntityTypeCode {
		return inlineCode(r.text[span.startByte:span.endByte])
	}
	if entity.Type == MessageEntityTypePre {
		if safeProjectionLanguage(entity.Language) != entity.Language {
			r.partial = true
		}
		return fencedBlock(r.text[span.startByte:span.endByte], entity.Language)
	}

	visible := r.renderRange(span.startByte, span.endByte, span.children)
	switch entity.Type {
	case MessageEntityTypeBold:
		if !r.canWrapInlineStyle(span, '*') {
			r.partial = true
			return visible
		}
		return "**" + visible + "**"
	case MessageEntityTypeItalic:
		if !r.canWrapInlineStyle(span, '*') {
			r.partial = true
			return visible
		}
		return "*" + visible + "*"
	case MessageEntityTypeStrikethrough:
		if !r.canWrapInlineStyle(span, '~') {
			r.partial = true
			return visible
		}
		return "~~" + visible + "~~"
	case MessageEntityTypeSpoiler:
		if !r.canWrapInlineStyle(span, '|') {
			r.partial = true
			return visible
		}
		return "||" + visible + "||"
	case MessageEntityTypeBlockquote, MessageEntityTypeExpandableBlockquote:
		return quoteMessageEntity(visible)
	case MessageEntityTypeTextLink:
		if entity.URL == "" {
			r.partial = true
			return visible
		}
		if strings.IndexFunc(entity.URL, unicode.IsControl) >= 0 {
			r.partial = true
		}
		return visibleWithTarget(visible, entity.URL)
	case MessageEntityTypeTextMention:
		if entity.User == nil || entity.User.ID == 0 {
			r.partial = true
			return visible
		}
		return visible + fmt.Sprintf(" [Telegram user id=%d]", entity.User.ID)
	case MessageEntityTypeDateTime:
		if strings.IndexFunc(entity.DateTimeFormat, unicode.IsControl) >= 0 {
			r.partial = true
		}
		return visible + fmt.Sprintf(" [time: unix=%d, format=%s]", entity.UnixTime, escapeMetadata(entity.DateTimeFormat))
	case MessageEntityTypeCustomEmoji:
		if entity.CustomEmojiID == "" {
			r.partial = true
		}
		return visible
	case MessageEntityTypeMention,
		MessageEntityTypeHashtag,
		MessageEntityTypeCashtag,
		MessageEntityTypeBotCommand,
		MessageEntityTypeURL,
		MessageEntityTypeEmail,
		MessageEntityTypePhoneNumber,
		MessageEntityTypeUnderline:
		return visible
	default:
		r.partial = true
		return visible
	}
}

func (r *messageEntityRenderer) canWrapInlineStyle(span *validatedMessageEntity, marker byte) bool {
	value := r.text[span.startByte:span.endByte]
	if strings.TrimSpace(value) != value || strings.IndexByte(value, marker) >= 0 {
		return false
	}
	if messageEntityChildrenContainType(span.children, span.entity.Type) {
		return false
	}
	if span.startByte > 0 && r.text[span.startByte-1] == marker {
		return false
	}
	if span.endByte < len(r.text) && r.text[span.endByte] == marker {
		return false
	}
	return !hasOddTrailingBackslashes(r.text[:span.startByte]) &&
		!hasOddTrailingBackslashes(value)
}

func messageEntityChildrenContainType(children []*validatedMessageEntity, kind MessageEntityType) bool {
	for _, child := range children {
		if child.entity.Type == kind || messageEntityChildrenContainType(child.children, kind) {
			return true
		}
	}
	return false
}

func hasOddTrailingBackslashes(value string) bool {
	count := 0
	for i := len(value) - 1; i >= 0 && value[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

func quoteMessageEntity(value string) string {
	var out strings.Builder
	out.Grow(len(value) + 2)
	out.WriteString("> ")
	for i := 0; i < len(value); i++ {
		out.WriteByte(value[i])
		switch value[i] {
		case '\n':
			out.WriteString("> ")
		case '\r':
			if i+1 >= len(value) || value[i+1] != '\n' {
				out.WriteString("> ")
			}
		}
	}
	return out.String()
}
