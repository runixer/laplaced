package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Internal safety budgets bound valid-but-adversarial entity arrays and the
// transport-neutral Markdown result. They are deliberately above Telegram's
// source-text ceiling and don't redefine any Bot API content limit.
const (
	RichProjectionMaxTextNodes        = 65_536
	RichProjectionMaxOutputCharacters = 1_048_576
)

// RichDisposition describes how completely a received rich message was
// understood. Partial and unsupported projections are still deterministic and
// never contain raw unknown JSON.
type RichDisposition string

const (
	RichDispositionAccepted    RichDisposition = "accepted"
	RichDispositionPartial     RichDisposition = "partial"
	RichDispositionUnsupported RichDisposition = "unsupported"
	RichDispositionInvalid     RichDisposition = "invalid"
)

// RichProjectionLimits controls the bounded semantic traversal. The defaults
// are Telegram's official limits plus a bounded diagnostics allowance.
type RichProjectionLimits struct {
	Characters       int
	Blocks           int
	NestingDepth     int
	Media            int
	TableColumns     int
	RichTextNodes    int
	OutputCharacters int
	UnknownKinds     int
}

// DefaultRichProjectionLimits returns Telegram Bot API 10.2 limits.
func DefaultRichProjectionLimits() RichProjectionLimits {
	return RichProjectionLimits{
		Characters:       RichMessageMaxCharacters,
		Blocks:           RichMessageMaxBlocks,
		NestingDepth:     RichMessageMaxNestingDepth,
		Media:            RichMessageMaxMedia,
		TableColumns:     RichMessageMaxTableColumns,
		RichTextNodes:    RichProjectionMaxTextNodes,
		OutputCharacters: RichProjectionMaxOutputCharacters,
		UnknownKinds:     32,
	}
}

// RichProjectionStats contains the semantic counts used for validation and
// low-cardinality observability.
type RichProjectionStats struct {
	Characters       int
	Blocks           int
	MaxDepth         int
	Media            int
	MaxTableColumns  int
	RichTextNodes    int
	OutputCharacters int
	Unknown          int
}

// RichValidationError is returned when an official structural limit or an
// internal traversal/output safety budget is exceeded. Field is stable and
// safe for metrics; no message content is kept.
type RichValidationError struct {
	Field  string
	Limit  int
	Actual int
}

func (e *RichValidationError) Error() string {
	return fmt.Sprintf("telegram rich message %s limit exceeded: %d > %d", e.Field, e.Actual, e.Limit)
}

// RichMediaKind identifies downloadable media blocks.
type RichMediaKind string

const (
	RichMediaPhoto     RichMediaKind = "photo"
	RichMediaVideo     RichMediaKind = "video"
	RichMediaAnimation RichMediaKind = "animation"
	RichMediaAudio     RichMediaKind = "audio"
	RichMediaVoice     RichMediaKind = "voice"
)

// RichMediaOccurrence preserves every media occurrence, even when multiple
// occurrences refer to the same Telegram file. Downstream code may coalesce
// downloads by a non-empty file_unique_id without losing order or captions.
type RichMediaOccurrence struct {
	Ordinal   int
	Marker    string
	Kind      RichMediaKind
	BlockPath string

	Caption string
	Credit  string

	ContainerPath    string
	ContainerCaption string
	ContainerCredit  string

	HasSpoiler bool
	Photo      []PhotoSize
	Video      *Video
	Animation  *Animation
	Audio      *Audio
	Voice      *Voice
}

// NormalizedRichMessage is a content-preserving, transport-neutral semantic
// projection. Markdown contains stable media/unsupported markers but never raw
// unknown JSON.
type NormalizedRichMessage struct {
	Markdown       string
	Media          []RichMediaOccurrence
	UnknownKinds   []string
	Disposition    RichDisposition
	HasVisibleText bool
	IsRTL          bool
	Stats          RichProjectionStats
}

// ProjectRichMessage projects with Telegram's official limits.
func ProjectRichMessage(message *RichMessage) (NormalizedRichMessage, error) {
	return ProjectRichMessageWithLimits(message, DefaultRichProjectionLimits())
}

// ProjectRichMessageWithLimits exists for exact boundary and fuzz tests. A
// production caller should normally use ProjectRichMessage.
func ProjectRichMessageWithLimits(message *RichMessage, limits RichProjectionLimits) (NormalizedRichMessage, error) {
	projector := richProjector{
		limits:      normalizeProjectionLimits(limits),
		unknownSeen: make(map[string]struct{}),
	}
	if message == nil {
		projector.result.Disposition = RichDispositionInvalid
		return projector.result, fmt.Errorf("telegram rich message is nil")
	}
	projector.result.IsRTL = message.IsRTL
	if message.Malformed {
		projector.partial = true
	}

	markdown, err := projector.projectBlocks(message.Blocks, 1, "blocks", mediaContext{})
	projector.result.Markdown = strings.TrimSpace(markdown)
	projector.result.HasVisibleText = projector.usableText
	projector.stats.OutputCharacters = utf8.RuneCountInString(projector.result.Markdown)
	projector.result.Stats = projector.stats
	if err != nil {
		projector.result.Disposition = RichDispositionInvalid
		return projector.result, err
	}
	if projector.stats.OutputCharacters > projector.limits.OutputCharacters {
		projector.result.Disposition = RichDispositionInvalid
		return projector.result, &RichValidationError{
			Field: "output_characters", Limit: projector.limits.OutputCharacters, Actual: projector.stats.OutputCharacters,
		}
	}

	switch {
	case !projector.usableText && len(projector.result.Media) == 0:
		projector.result.Disposition = RichDispositionUnsupported
	case projector.partial:
		projector.result.Disposition = RichDispositionPartial
	default:
		projector.result.Disposition = RichDispositionAccepted
	}
	return projector.result, nil
}

func normalizeProjectionLimits(limits RichProjectionLimits) RichProjectionLimits {
	defaults := DefaultRichProjectionLimits()
	if limits.Characters <= 0 {
		limits.Characters = defaults.Characters
	}
	if limits.Blocks <= 0 {
		limits.Blocks = defaults.Blocks
	}
	if limits.NestingDepth <= 0 {
		limits.NestingDepth = defaults.NestingDepth
	}
	if limits.Media <= 0 {
		limits.Media = defaults.Media
	}
	if limits.TableColumns <= 0 {
		limits.TableColumns = defaults.TableColumns
	}
	if limits.RichTextNodes <= 0 {
		limits.RichTextNodes = defaults.RichTextNodes
	}
	if limits.OutputCharacters <= 0 {
		limits.OutputCharacters = defaults.OutputCharacters
	}
	if limits.UnknownKinds <= 0 {
		limits.UnknownKinds = defaults.UnknownKinds
	}
	return limits
}

type richProjector struct {
	limits RichProjectionLimits
	stats  RichProjectionStats
	result NormalizedRichMessage

	unknownSeen map[string]struct{}
	partial     bool
	usableText  bool
}

type richTextMode uint8

const (
	richTextMarkdown richTextMode = iota
	richTextLiteral
)

type mediaContext struct {
	path    string
	caption string
	credit  string
}

func (p *richProjector) projectBlocks(blocks []RichBlock, depth int, path string, context mediaContext) (string, error) {
	parts := make([]string, 0, len(blocks))
	for i := range blocks {
		projected, err := p.projectBlock(&blocks[i], depth, fmt.Sprintf("%s[%d]", path, i), context)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(projected) != "" {
			parts = append(parts, strings.TrimSpace(projected))
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func (p *richProjector) projectBlock(block *RichBlock, depth int, path string, context mediaContext) (string, error) {
	if err := p.checkDepth(depth); err != nil {
		return "", err
	}
	if err := p.addBlocks(1); err != nil {
		return "", err
	}
	if block.Malformed {
		p.partial = true
	}
	if block.Unknown {
		return p.projectUnknownBlock(block, depth, path, context)
	}

	switch block.Type {
	case RichBlockParagraph:
		return p.projectText(block.Text, depth+1, richTextMarkdown)
	case RichBlockHeading:
		text, err := p.projectText(block.Text, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		size := block.Size
		if size < 1 || size > 6 {
			p.partial = true
			size = 3
		}
		return strings.Repeat("#", size) + " " + text, nil
	case RichBlockPreformatted:
		text, err := p.projectText(block.Text, depth+1, richTextLiteral)
		if err != nil {
			return "", err
		}
		return fencedBlock(text, block.Language), nil
	case RichBlockFooter:
		text, err := p.projectText(block.Text, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		return "Footer: " + text, nil
	case RichBlockDivider:
		return "---", nil
	case RichBlockMathematicalExpression:
		if err := p.addCharacters(block.Expression); err != nil {
			return "", err
		}
		return "$$\n" + escapeMathDelimiter(block.Expression) + "\n$$", nil
	case RichBlockAnchor:
		// Anchors are positional metadata and intentionally produce no visible
		// junk. Anchor links retain the relationship at their use site.
		return "", nil
	case RichBlockList:
		return p.projectListItems(block.Items, depth+1, path+".items", context)
	case RichBlockBlockquote:
		body, err := p.projectBlocks(block.Blocks, depth+1, path+".blocks", context)
		if err != nil {
			return "", err
		}
		credit, err := p.projectText(block.Credit, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		if credit != "" {
			body = strings.TrimSpace(body) + "\n\n— " + credit
		}
		return quoteMarkdown(body), nil
	case RichBlockPullquote:
		text, err := p.projectText(block.Text, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		credit, err := p.projectText(block.Credit, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		if credit != "" {
			text += "\n\n— " + credit
		}
		return quoteMarkdown(text), nil
	case RichBlockCollage, RichBlockSlideshow:
		container := mediaContext{
			path:    path,
			caption: plainCaptionText(block.Caption),
			credit:  plainCaptionCredit(block.Caption),
		}
		body, err := p.projectBlocks(block.Blocks, depth+1, path+".blocks", container)
		if err != nil {
			return "", err
		}
		caption, err := p.projectCaption(block.Caption, depth+1)
		if err != nil {
			return "", err
		}
		return joinNonEmpty(body, caption), nil
	case RichBlockTable:
		return p.projectTable(block, depth, path)
	case RichBlockDetails:
		summary, err := p.projectText(block.Summary, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		body, err := p.projectBlocks(block.Blocks, depth+1, path+".blocks", context)
		if err != nil {
			return "", err
		}
		state := "closed"
		if block.IsOpen {
			state = "open"
		}
		return "**Details (" + state + "):** " + summary + joinWithBlankLine(body), nil
	case RichBlockMap:
		if block.Location == nil {
			p.partial = true
			return unsupportedMarker("map-without-location"), nil
		}
		marker := fmt.Sprintf("[Map: latitude=%s, longitude=%s, zoom=%d, width=%d, height=%d]",
			formatFloat(block.Location.Latitude), formatFloat(block.Location.Longitude), block.Zoom,
			block.Width, block.Height)
		// Map metadata is useful visible content, but isn't source rich-message
		// text and therefore must not consume Telegram's character limit.
		p.usableText = true
		caption, err := p.projectCaption(block.Caption, depth+1)
		if err != nil {
			return "", err
		}
		return joinNonEmpty(marker, caption), nil
	case RichBlockAnimation:
		return p.projectMedia(block, RichMediaAnimation, depth, path, context)
	case RichBlockAudio:
		return p.projectMedia(block, RichMediaAudio, depth, path, context)
	case RichBlockPhoto:
		return p.projectMedia(block, RichMediaPhoto, depth, path, context)
	case RichBlockVideo:
		return p.projectMedia(block, RichMediaVideo, depth, path, context)
	case RichBlockVoiceNote:
		return p.projectMedia(block, RichMediaVoice, depth, path, context)
	case RichBlockThinking:
		p.partial = true // thinking is draft-only and anomalous in final ingress.
		text, err := p.projectText(block.Text, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		return "[Thinking] " + text, nil
	default:
		p.partial = true
		p.recordUnknown("block:" + string(block.Type))
		return unsupportedMarker(string(block.Type)), nil
	}
}

func (p *richProjector) projectUnknownBlock(block *RichBlock, depth int, path string, context mediaContext) (string, error) {
	p.partial = true
	p.recordUnknown("block:" + string(block.Type))
	parts := []string{unsupportedMarker(string(block.Type))}
	for _, text := range []*RichText{block.Text, block.Summary} {
		value, err := p.projectText(text, depth+1, richTextMarkdown)
		if err != nil {
			return "", err
		}
		if value != "" {
			parts = append(parts, value)
		}
	}
	if len(block.Blocks) > 0 {
		value, err := p.projectBlocks(block.Blocks, depth+1, path+".blocks", context)
		if err != nil {
			return "", err
		}
		parts = append(parts, value)
	}
	if len(block.Items) > 0 {
		value, err := p.projectListItems(block.Items, depth+1, path+".items", context)
		if err != nil {
			return "", err
		}
		parts = append(parts, value)
	}
	if len(block.Cells) > 0 {
		value, err := p.projectUnknownTable(block.Cells, depth+1, path+".cells")
		if err != nil {
			return "", err
		}
		parts = append(parts, value)
	}
	caption, err := p.projectCaption(block.Caption, depth+1)
	if err != nil {
		return "", err
	}
	parts = append(parts, caption)
	return joinNonEmpty(parts...), nil
}

func (p *richProjector) projectText(text *RichText, depth int, mode richTextMode) (string, error) {
	if text == nil {
		return "", nil
	}
	if err := p.addRichTextNode(); err != nil {
		return "", err
	}
	if err := p.checkDepth(depth); err != nil {
		return "", err
	}
	if text.Malformed {
		p.partial = true
	}
	if text.Unknown {
		p.partial = true
		p.recordUnknown("text:" + string(text.Kind))
		visible, err := p.projectText(text.Text, depth+1, mode)
		if err != nil {
			return "", err
		}
		if visible != "" {
			return visible, nil
		}
		if mode == richTextLiteral {
			return "", nil
		}
		return unsupportedInlineMarker(string(text.Kind)), nil
	}

	switch text.Kind {
	case RichTextPlain:
		if err := p.addCharacters(text.Value); err != nil {
			return "", err
		}
		if mode == richTextLiteral {
			return text.Value, nil
		}
		return escapeMarkdownText(text.Value), nil
	case RichTextArray:
		var out strings.Builder
		for i := range text.Children {
			value, err := p.projectText(&text.Children[i], depth+1, mode)
			if err != nil {
				return "", err
			}
			out.WriteString(value)
		}
		return out.String(), nil
	case RichTextCustomEmoji:
		if err := p.addCharacters(text.AlternativeText); err != nil {
			return "", err
		}
		if mode == richTextLiteral {
			return text.AlternativeText, nil
		}
		return escapeMarkdownText(text.AlternativeText), nil
	case RichTextMathematicalExpression:
		if err := p.addCharacters(text.Expression); err != nil {
			return "", err
		}
		if mode == richTextLiteral {
			return text.Expression, nil
		}
		return "$" + escapeMathDelimiter(text.Expression) + "$", nil
	case RichTextAnchor:
		return "", nil
	}

	childMode := mode
	if text.Kind == RichTextCode {
		childMode = richTextLiteral
	}
	visible, err := p.projectText(text.Text, depth+1, childMode)
	if err != nil {
		return "", err
	}
	visible = p.sensitiveVisibleLabel(text.Kind, visible)
	if mode == richTextLiteral {
		return visible, nil
	}

	switch text.Kind {
	case RichTextBold:
		return "**" + visible + "**", nil
	case RichTextItalic:
		return "*" + visible + "*", nil
	case RichTextStrikethrough:
		return "~~" + visible + "~~", nil
	case RichTextSpoiler:
		return "||" + visible + "||", nil
	case RichTextCode:
		return inlineCode(visible), nil
	case RichTextUnderline, RichTextSubscript, RichTextSuperscript, RichTextMarked:
		return visible, nil
	case RichTextDateTime:
		return visible + fmt.Sprintf(" [time: unix=%d, format=%s]", text.UnixTime, escapeMetadata(text.DateTimeFormat)), nil
	case RichTextTextMention:
		if text.User == nil {
			p.partial = true
			return visible, nil
		}
		return visible + fmt.Sprintf(" [Telegram user id=%d]", text.User.ID), nil
	case RichTextURL:
		return visibleWithTarget(visible, text.URL), nil
	case RichTextEmailAddress, RichTextPhoneNumber, RichTextBankCardNumber:
		// These typed targets may contain a value that is intentionally hidden or
		// masked by the visible label. Never expand the target into model input,
		// history, RAG text or trace previews.
		return visible, nil
	case RichTextMention:
		return visibleWithMetadata(visible, "username", text.Username), nil
	case RichTextHashtag:
		return visibleWithMetadata(visible, "hashtag", text.Hashtag), nil
	case RichTextCashtag:
		return visibleWithMetadata(visible, "cashtag", text.Cashtag), nil
	case RichTextBotCommand:
		return visibleWithMetadata(visible, "command", text.BotCommand), nil
	case RichTextAnchorLink:
		return visibleWithMetadata(visible, "anchor", text.AnchorName), nil
	case RichTextReference:
		return visibleWithMetadata(visible, "reference", text.Name), nil
	case RichTextReferenceLink:
		return visibleWithMetadata(visible, "reference", text.ReferenceName), nil
	default:
		p.partial = true
		p.recordUnknown("text:" + string(text.Kind))
		return visible, nil
	}
}

func (p *richProjector) sensitiveVisibleLabel(kind RichTextKind, visible string) string {
	if strings.TrimSpace(visible) != "" {
		return visible
	}
	var placeholder string
	switch kind {
	case RichTextEmailAddress:
		placeholder = "[email address]"
	case RichTextPhoneNumber:
		placeholder = "[phone number]"
	case RichTextBankCardNumber:
		placeholder = "[bank card]"
	default:
		return visible
	}
	// A typed entity without a visible label is still usable semantic input, but
	// the fixed placeholder does not count against Telegram's source-character
	// limit. It is covered by the independent projected-output budget.
	p.partial = true
	p.usableText = true
	return placeholder
}

func (p *richProjector) projectListItems(items []RichBlockListItem, depth int, path string, context mediaContext) (string, error) {
	lines := make([]string, 0, len(items))
	for i := range items {
		item := &items[i]
		if err := p.checkDepth(depth); err != nil {
			return "", err
		}
		if err := p.addBlocks(1); err != nil {
			return "", err
		}
		if item.Malformed {
			p.partial = true
		}
		if item.Label != "" {
			if err := p.addCharacters(item.Label); err != nil {
				return "", err
			}
		}
		body, err := p.projectBlocks(item.Blocks, depth+1, fmt.Sprintf("%s[%d].blocks", path, i), context)
		if err != nil {
			return "", err
		}
		marker := "-"
		prefix := ""
		switch {
		case item.HasCheckbox:
			if item.IsChecked {
				marker = "- [x]"
			} else {
				marker = "- [ ]"
			}
		case item.Value != nil:
			marker = richListItemMarker(item)
		case strings.TrimSpace(item.Label) != "" && strings.TrimSpace(item.Label) != "•":
			prefix = "[label: " + escapeMarkdownText(strings.TrimSpace(item.Label)) + "] "
		}
		body = prefix + strings.TrimSpace(body)
		lines = append(lines, marker+" "+indentContinuation(body, "  "))
	}
	return strings.Join(lines, "\n"), nil
}

func richListItemMarker(item *RichBlockListItem) string {
	if item == nil || item.Value == nil {
		return "-"
	}
	if item.Type == "" || item.Type == "1" {
		return strconv.Itoa(*item.Value) + "."
	}
	label := strings.Join(strings.Fields(item.Label), " ")
	if label != "" {
		return escapeMarkdownText(label)
	}
	// A malformed direct value without Telegram's required rendered label is
	// still represented deterministically; the Malformed flag makes it partial.
	return strconv.Itoa(*item.Value) + "."
}

func (p *richProjector) projectTable(block *RichBlock, depth int, path string) (string, error) {
	caption, err := p.projectText(block.Text, depth+1, richTextMarkdown)
	if err != nil {
		return "", err
	}
	rows := make([][]string, 0, len(block.Cells))
	complex := len(block.Cells) == 0
	columns := -1
	var activeRowspans []int
	for rowIndex, row := range block.Cells {
		rowDepth := depth + 1
		if err := p.checkDepth(rowDepth); err != nil {
			return "", err
		}
		if err := p.addBlocks(1); err != nil {
			return "", err
		}
		width, nextRowspans, exceeded := richTableRowWidth(row, activeRowspans, p.limits.TableColumns)
		activeRowspans = nextRowspans
		if width > p.stats.MaxTableColumns {
			p.stats.MaxTableColumns = width
		}
		if exceeded > 0 {
			return "", &RichValidationError{Field: "table_columns", Limit: p.limits.TableColumns, Actual: exceeded}
		}
		values := make([]string, 0, len(row))
		allHeader := len(row) > 0
		for cellIndex := range row {
			cell := &row[cellIndex]
			if cell.Malformed {
				p.partial = true
			}
			span := cell.Colspan
			if span <= 0 {
				span = 1
			}
			if span > 1 || cell.Rowspan > 1 {
				complex = true
			}
			allHeader = allHeader && cell.IsHeader
			if cell.VAlign != "" && cell.VAlign != "top" {
				complex = true
			}
			if rowIndex > 0 && cellIndex < len(block.Cells[0]) &&
				normalizeTableAlign(cell.Align) != normalizeTableAlign(block.Cells[0][cellIndex].Align) {
				complex = true
			}
			value, textErr := p.projectText(cell.Text, rowDepth+1, richTextMarkdown)
			if textErr != nil {
				return "", textErr
			}
			values = append(values, strings.ReplaceAll(value, "\n", "<br>"))
		}
		if columns < 0 {
			columns = width
		} else if columns != width {
			complex = true
		}
		if rowIndex == 0 && !allHeader {
			complex = true
		}
		rows = append(rows, values)
	}

	var table string
	if complex {
		table = renderTableFallback(block.Cells, rows)
	} else {
		table = renderGFMTable(block.Cells[0], rows)
	}
	if caption != "" {
		table = "Table: " + caption + "\n\n" + table
	}
	_ = path // retained in the signature for stable future cell diagnostics.
	return table, nil
}

func (p *richProjector) projectUnknownTable(cells [][]RichBlockTableCell, depth int, path string) (string, error) {
	block := &RichBlock{Type: RichBlockTable, Cells: cells}
	// The unknown parent already consumed its own block count; the synthetic
	// table must account only for rows, so compensate for projectTable's caller
	// convention by projecting rows directly.
	rows := make([][]string, 0, len(cells))
	var activeRowspans []int
	for rowIndex, row := range cells {
		if err := p.checkDepth(depth); err != nil {
			return "", err
		}
		if err := p.addBlocks(1); err != nil {
			return "", err
		}
		width, nextRowspans, exceeded := richTableRowWidth(row, activeRowspans, p.limits.TableColumns)
		activeRowspans = nextRowspans
		if width > p.stats.MaxTableColumns {
			p.stats.MaxTableColumns = width
		}
		if exceeded > 0 {
			return "", &RichValidationError{Field: "table_columns", Limit: p.limits.TableColumns, Actual: exceeded}
		}
		values := make([]string, 0, len(row))
		for cellIndex := range row {
			cell := &row[cellIndex]
			value, err := p.projectText(cell.Text, depth+1, richTextMarkdown)
			if err != nil {
				return "", err
			}
			values = append(values, value)
		}
		rows = append(rows, values)
		_ = rowIndex
	}
	_ = block
	_ = path
	return renderTableFallback(cells, rows), nil
}

// richTableRowWidth applies HTML's table placement rule: cells occupy the
// first contiguous free columns, skipping columns held by rowspans from prior
// rows. It never allocates beyond limit columns, even for a hostile colspan.
func richTableRowWidth(row []RichBlockTableCell, active []int, limit int) (int, []int, int) {
	next := make([]int, len(active))
	width := 0
	for column, remaining := range active {
		if remaining <= 0 {
			continue
		}
		width = column + 1
		if remaining > 1 {
			next[column] = remaining - 1
		}
	}

	cursor := 0
	for i := range row {
		span := row[i].Colspan
		if span <= 0 {
			span = 1
		}
		for {
			for cursor < len(active) && active[cursor] > 0 {
				cursor++
			}
			end := cursor + span
			if end < cursor || end > limit {
				return maxInt(width, limit+1), next, maxInt(end, limit+1)
			}
			conflict := -1
			for column := cursor; column < end && column < len(active); column++ {
				if active[column] > 0 {
					conflict = column
					break
				}
			}
			if conflict >= 0 {
				cursor = conflict + 1
				continue
			}
			if end > len(next) {
				next = append(next, make([]int, end-len(next))...)
			}
			rowspan := row[i].Rowspan
			if rowspan > 1 {
				for column := cursor; column < end; column++ {
					next[column] = rowspan - 1
				}
			}
			width = maxInt(width, end)
			cursor = end
			break
		}
	}

	for len(next) > 0 && next[len(next)-1] == 0 {
		next = next[:len(next)-1]
	}
	return width, next, 0
}

func (p *richProjector) projectCaption(caption *RichBlockCaption, depth int) (string, error) {
	if caption == nil {
		return "", nil
	}
	if caption.Malformed {
		p.partial = true
	}
	text, err := p.projectText(caption.Text, depth+1, richTextMarkdown)
	if err != nil {
		return "", err
	}
	credit, err := p.projectText(caption.Credit, depth+1, richTextMarkdown)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 2)
	if text != "" {
		parts = append(parts, "Caption: "+text)
	}
	if credit != "" {
		parts = append(parts, "Credit: "+credit)
	}
	return strings.Join(parts, "\n"), nil
}

func (p *richProjector) projectMedia(block *RichBlock, kind RichMediaKind, depth int, path string, context mediaContext) (string, error) {
	if err := p.addMedia(); err != nil {
		return "", err
	}
	if !richMediaHasSource(block, kind) {
		p.partial = true
		p.recordUnknown("media:" + string(kind) + ":missing_source")
	}
	ordinal := len(p.result.Media) + 1
	marker := fmt.Sprintf("[[telegram-rich-media:%d:%s]]", ordinal, kind)
	occurrence := RichMediaOccurrence{
		Ordinal:          ordinal,
		Marker:           marker,
		Kind:             kind,
		BlockPath:        path,
		Caption:          plainCaptionText(block.Caption),
		Credit:           plainCaptionCredit(block.Caption),
		ContainerPath:    context.path,
		ContainerCaption: context.caption,
		ContainerCredit:  context.credit,
		HasSpoiler:       block.HasSpoiler,
		Photo:            append([]PhotoSize(nil), block.Photo...),
		Video:            cloneRichVideo(block.Video),
		Animation:        cloneRichAnimation(block.Animation),
		Audio:            cloneRichAudio(block.Audio),
		Voice:            cloneRichVoice(block.VoiceNote),
	}
	p.result.Media = append(p.result.Media, occurrence)
	caption, err := p.projectCaption(block.Caption, depth+1)
	if err != nil {
		return "", err
	}
	return joinNonEmpty(marker, caption), nil
}

func cloneRichPhotoSize(photo *PhotoSize) *PhotoSize {
	if photo == nil {
		return nil
	}
	cloned := *photo
	return &cloned
}

func cloneRichVideo(video *Video) *Video {
	if video == nil {
		return nil
	}
	cloned := *video
	cloned.Thumbnail = cloneRichPhotoSize(video.Thumbnail)
	cloned.Cover = append([]PhotoSize(nil), video.Cover...)
	cloned.Qualities = append([]VideoQuality(nil), video.Qualities...)
	return &cloned
}

func cloneRichAnimation(animation *Animation) *Animation {
	if animation == nil {
		return nil
	}
	cloned := *animation
	cloned.Thumbnail = cloneRichPhotoSize(animation.Thumbnail)
	return &cloned
}

func cloneRichAudio(audio *Audio) *Audio {
	if audio == nil {
		return nil
	}
	cloned := *audio
	cloned.Thumbnail = cloneRichPhotoSize(audio.Thumbnail)
	return &cloned
}

func cloneRichVoice(voice *Voice) *Voice {
	if voice == nil {
		return nil
	}
	cloned := *voice
	return &cloned
}

func richMediaHasSource(block *RichBlock, kind RichMediaKind) bool {
	switch kind {
	case RichMediaPhoto:
		for i := range block.Photo {
			if block.Photo[i].FileID != "" {
				return true
			}
		}
		return false
	case RichMediaVideo:
		return block.Video != nil && block.Video.FileID != ""
	case RichMediaAnimation:
		return block.Animation != nil && block.Animation.FileID != ""
	case RichMediaAudio:
		return block.Audio != nil && block.Audio.FileID != ""
	case RichMediaVoice:
		return block.VoiceNote != nil && block.VoiceNote.FileID != ""
	default:
		return false
	}
}

func (p *richProjector) addCharacters(value string) error {
	count := utf8.RuneCountInString(value)
	if count > 0 {
		p.usableText = true
	}
	p.stats.Characters += count
	if p.stats.Characters > p.limits.Characters {
		return &RichValidationError{Field: "characters", Limit: p.limits.Characters, Actual: p.stats.Characters}
	}
	return nil
}

func (p *richProjector) addBlocks(count int) error {
	p.stats.Blocks += count
	if p.stats.Blocks > p.limits.Blocks {
		return &RichValidationError{Field: "blocks", Limit: p.limits.Blocks, Actual: p.stats.Blocks}
	}
	return nil
}

func (p *richProjector) addMedia() error {
	p.stats.Media++
	if p.stats.Media > p.limits.Media {
		return &RichValidationError{Field: "media", Limit: p.limits.Media, Actual: p.stats.Media}
	}
	return nil
}

func (p *richProjector) addRichTextNode() error {
	p.stats.RichTextNodes++
	if p.stats.RichTextNodes > p.limits.RichTextNodes {
		return &RichValidationError{Field: "rich_text_nodes", Limit: p.limits.RichTextNodes, Actual: p.stats.RichTextNodes}
	}
	return nil
}

func (p *richProjector) checkDepth(depth int) error {
	if depth > p.stats.MaxDepth {
		p.stats.MaxDepth = depth
	}
	if depth > p.limits.NestingDepth {
		return &RichValidationError{Field: "nesting_depth", Limit: p.limits.NestingDepth, Actual: depth}
	}
	return nil
}

func (p *richProjector) recordUnknown(kind string) {
	p.stats.Unknown++
	kind = sanitizeRichDiscriminator(kind)
	if _, exists := p.unknownSeen[kind]; exists {
		return
	}
	p.unknownSeen[kind] = struct{}{}
	if len(p.result.UnknownKinds) < p.limits.UnknownKinds {
		p.result.UnknownKinds = append(p.result.UnknownKinds, kind)
	}
}

func escapeMarkdownText(value string) string {
	const special = `\\` + "`*_{}[]<>()#+-.!|~$"
	var out strings.Builder
	out.Grow(len(value))
	for _, r := range value {
		if strings.ContainsRune(special, r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func escapeMathDelimiter(value string) string {
	return strings.ReplaceAll(value, "$", `\$`)
}

func escapeMetadata(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	return escapeMarkdownText(value)
}

func visibleWithTarget(visible, target string) string {
	if target == "" || target == plainUnescape(visible) {
		return visible
	}
	// Targets are inert model metadata, never active Markdown links. Remove
	// control characters as well as escaping Markdown punctuation so an
	// adversarial target cannot inject a new line or projected structure.
	return visible + " (URL: " + escapeMetadata(target) + ")"
}

func visibleWithMetadata(visible, name, value string) string {
	if value == "" || value == plainUnescape(visible) {
		return visible
	}
	return visible + " [" + name + ": " + escapeMetadata(value) + "]"
}

func plainUnescape(value string) string {
	return strings.ReplaceAll(value, `\`, "")
}

func inlineCode(value string) string {
	fence := strings.Repeat("`", maxInt(1, longestRun(value, '`')+1))
	return fence + " " + value + " " + fence
}

func fencedBlock(value, language string) string {
	fence := strings.Repeat("`", maxInt(3, longestRun(value, '`')+1))
	language = safeProjectionLanguage(language)
	return fence + language + "\n" + value + "\n" + fence
}

func safeProjectionLanguage(value string) string {
	if len(value) > 32 {
		return ""
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '+' && r != '-' {
			return ""
		}
	}
	return value
}

func longestRun(value string, marker rune) int {
	longest, current := 0, 0
	for _, r := range value {
		if r == marker {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return longest
}

func quoteMarkdown(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

func indentContinuation(value, indent string) string {
	return strings.ReplaceAll(value, "\n", "\n"+indent)
}

func renderGFMTable(header []RichBlockTableCell, rows [][]string) string {
	var out strings.Builder
	writeTableRow := func(values []string) {
		out.WriteString("| ")
		out.WriteString(strings.Join(values, " | "))
		out.WriteString(" |\n")
	}
	writeTableRow(rows[0])
	alignments := make([]string, len(header))
	for i, cell := range header {
		switch cell.Align {
		case "center":
			alignments[i] = ":---:"
		case "right":
			alignments[i] = "---:"
		default:
			alignments[i] = "---"
		}
	}
	writeTableRow(alignments)
	for _, row := range rows[1:] {
		writeTableRow(row)
	}
	return strings.TrimSpace(out.String())
}

func normalizeTableAlign(value string) string {
	if value == "" {
		return "left"
	}
	return value
}

func renderTableFallback(cells [][]RichBlockTableCell, rows [][]string) string {
	var out strings.Builder
	out.WriteString("Table (row-by-row):")
	for rowIndex, row := range rows {
		out.WriteString("\nRow ")
		out.WriteString(strconv.Itoa(rowIndex + 1))
		out.WriteString(":")
		for cellIndex, value := range row {
			cell := cells[rowIndex][cellIndex]
			out.WriteString("\n- Cell ")
			out.WriteString(strconv.Itoa(cellIndex + 1))
			attrs := make([]string, 0, 3)
			if cell.IsHeader {
				attrs = append(attrs, "header")
			}
			if cell.Colspan > 1 {
				attrs = append(attrs, "colspan="+strconv.Itoa(cell.Colspan))
			}
			if cell.Rowspan > 1 {
				attrs = append(attrs, "rowspan="+strconv.Itoa(cell.Rowspan))
			}
			if cell.Align != "" {
				attrs = append(attrs, "align="+escapeMetadata(cell.Align))
			}
			if cell.VAlign != "" {
				attrs = append(attrs, "valign="+escapeMetadata(cell.VAlign))
			}
			if len(attrs) > 0 {
				out.WriteString(" (")
				out.WriteString(strings.Join(attrs, ", "))
				out.WriteString(")")
			}
			out.WriteString(": ")
			out.WriteString(value)
		}
	}
	return out.String()
}

func plainCaptionText(caption *RichBlockCaption) string {
	if caption == nil {
		return ""
	}
	return plainRichText(caption.Text)
}

func plainCaptionCredit(caption *RichBlockCaption) string {
	if caption == nil {
		return ""
	}
	return plainRichText(caption.Credit)
}

func plainRichText(text *RichText) string {
	if text == nil {
		return ""
	}
	switch text.Kind {
	case RichTextPlain:
		return text.Value
	case RichTextArray:
		var out strings.Builder
		for i := range text.Children {
			out.WriteString(plainRichText(&text.Children[i]))
		}
		return out.String()
	case RichTextCustomEmoji:
		return text.AlternativeText
	case RichTextMathematicalExpression:
		return text.Expression
	case RichTextAnchor:
		return ""
	default:
		return plainRichText(text.Text)
	}
}

func unsupportedMarker(kind string) string {
	return "[[telegram-rich-unsupported:" + sanitizeRichDiscriminator(kind) + "]]"
}

func unsupportedInlineMarker(kind string) string {
	return "[unsupported rich text: " + sanitizeRichDiscriminator(kind) + "]"
}

func joinWithBlankLine(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "\n\n" + strings.TrimSpace(value)
}

func joinNonEmpty(values ...string) string {
	nonEmpty := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(value))
		}
	}
	return strings.Join(nonEmpty, "\n")
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
