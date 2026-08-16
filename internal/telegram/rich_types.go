package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Telegram Bot API 10.2 limits for received rich messages. The decoder is
// deliberately tolerant; these limits are enforced by ProjectRichMessage so an
// unknown discriminator doesn't make encoding/json discard the whole Update.
const (
	RichMessageMaxCharacters   = 32_768
	RichMessageMaxBlocks       = 500
	RichMessageMaxNestingDepth = 16
	RichMessageMaxMedia        = 50
	RichMessageMaxTableColumns = 20

	richDecodeHardDepth = RichMessageMaxNestingDepth + 2
)

// RichTextKind identifies one member of Telegram's recursive RichText union.
// Plain and array are internal tags for the two untagged JSON alternatives.
type RichTextKind string

const (
	RichTextPlain                  RichTextKind = "plain"
	RichTextArray                  RichTextKind = "array"
	RichTextBold                   RichTextKind = "bold"
	RichTextItalic                 RichTextKind = "italic"
	RichTextUnderline              RichTextKind = "underline"
	RichTextStrikethrough          RichTextKind = "strikethrough"
	RichTextSpoiler                RichTextKind = "spoiler"
	RichTextDateTime               RichTextKind = "date_time"
	RichTextTextMention            RichTextKind = "text_mention"
	RichTextSubscript              RichTextKind = "subscript"
	RichTextSuperscript            RichTextKind = "superscript"
	RichTextMarked                 RichTextKind = "marked"
	RichTextCode                   RichTextKind = "code"
	RichTextCustomEmoji            RichTextKind = "custom_emoji"
	RichTextMathematicalExpression RichTextKind = "mathematical_expression"
	RichTextURL                    RichTextKind = "url"
	RichTextEmailAddress           RichTextKind = "email_address"
	RichTextPhoneNumber            RichTextKind = "phone_number"
	RichTextBankCardNumber         RichTextKind = "bank_card_number"
	RichTextMention                RichTextKind = "mention"
	RichTextHashtag                RichTextKind = "hashtag"
	RichTextCashtag                RichTextKind = "cashtag"
	RichTextBotCommand             RichTextKind = "bot_command"
	RichTextAnchor                 RichTextKind = "anchor"
	RichTextAnchorLink             RichTextKind = "anchor_link"
	RichTextReference              RichTextKind = "reference"
	RichTextReferenceLink          RichTextKind = "reference_link"
)

// RichText is a lossless representation of all documented RichText variants,
// except that unknown raw JSON is intentionally discarded. For an unknown
// object, Type and a recursively decoded human-readable text field are retained
// for bounded semantic salvage.
type RichText struct {
	Kind     RichTextKind
	Value    string
	Children []RichText
	Text     *RichText

	UnixTime       int64
	DateTimeFormat string
	User           *User

	CustomEmojiID   string
	AlternativeText string
	Expression      string
	URL             string
	EmailAddress    string
	PhoneNumber     string
	BankCardNumber  string
	Username        string
	Hashtag         string
	Cashtag         string
	BotCommand      string
	Name            string
	AnchorName      string
	ReferenceName   string

	Unknown   bool
	Malformed bool
}

// UnmarshalJSON implements the string/array/tagged-object RichText union.
func (t *RichText) UnmarshalJSON(data []byte) error {
	decoded, err := decodeRichText(data, 1)
	if err != nil {
		return err
	}
	*t = decoded
	return nil
}

// RichBlockType identifies one member of Telegram's RichBlock union.
type RichBlockType string

const (
	RichBlockParagraph              RichBlockType = "paragraph"
	RichBlockHeading                RichBlockType = "heading"
	RichBlockPreformatted           RichBlockType = "pre"
	RichBlockFooter                 RichBlockType = "footer"
	RichBlockDivider                RichBlockType = "divider"
	RichBlockMathematicalExpression RichBlockType = "mathematical_expression"
	RichBlockAnchor                 RichBlockType = "anchor"
	RichBlockList                   RichBlockType = "list"
	RichBlockBlockquote             RichBlockType = "blockquote"
	RichBlockPullquote              RichBlockType = "pullquote"
	RichBlockCollage                RichBlockType = "collage"
	RichBlockSlideshow              RichBlockType = "slideshow"
	RichBlockTable                  RichBlockType = "table"
	RichBlockDetails                RichBlockType = "details"
	RichBlockMap                    RichBlockType = "map"
	RichBlockAnimation              RichBlockType = "animation"
	RichBlockAudio                  RichBlockType = "audio"
	RichBlockPhoto                  RichBlockType = "photo"
	RichBlockVideo                  RichBlockType = "video"
	RichBlockVoiceNote              RichBlockType = "voice_note"
	RichBlockThinking               RichBlockType = "thinking"
)

// RichBlockCaption is used by media, map, collage and slideshow blocks.
type RichBlockCaption struct {
	Text   *RichText
	Credit *RichText

	Malformed bool
}

// RichBlockTableCell is one cell in a received rich table.
type RichBlockTableCell struct {
	Text     *RichText
	IsHeader bool
	Colspan  int
	Rowspan  int
	Align    string
	VAlign   string

	Malformed bool
}

// RichBlockListItem is one received list item. Value is a pointer because zero
// is distinct from an omitted value in the Bot API schema.
type RichBlockListItem struct {
	Label       string
	Blocks      []RichBlock
	HasCheckbox bool
	IsChecked   bool
	Value       *int
	Type        string

	Malformed bool
}

// RichBlock contains fields for all Bot API 10.2 received block variants.
// Unknown blocks retain only safe, documented human-readable shapes; arbitrary
// fields and raw JSON never leave the decoder.
type RichBlock struct {
	Type RichBlockType

	Text       *RichText
	Size       int
	Language   string
	Expression string
	Name       string

	Items   []RichBlockListItem
	Blocks  []RichBlock
	Credit  *RichText
	Caption *RichBlockCaption

	Cells      [][]RichBlockTableCell
	IsBordered bool
	IsStriped  bool

	Summary *RichText
	IsOpen  bool

	Location *Location
	Zoom     int
	Width    int
	Height   int

	Animation  *Animation
	Audio      *Audio
	Photo      []PhotoSize
	Video      *Video
	VoiceNote  *Voice
	HasSpoiler bool

	Unknown   bool
	Malformed bool
}

// UnmarshalJSON implements the tagged RichBlock union without retaining raw
// JSON for unknown variants.
func (b *RichBlock) UnmarshalJSON(data []byte) error {
	decoded, err := decodeRichBlock(data, 1)
	if err != nil {
		return err
	}
	*b = decoded
	return nil
}

// RichMessage is Telegram's canonical received rich-message representation.
type RichMessage struct {
	Blocks []RichBlock `json:"-"`
	IsRTL  bool        `json:"-"`

	Malformed bool `json:"-"`
}

// UnmarshalJSON decodes each block through the tolerant union decoder.
func (m *RichMessage) UnmarshalJSON(data []byte) error {
	obj, err := decodeJSONObject(data)
	if err != nil {
		if json.Valid(data) {
			*m = RichMessage{Malformed: true}
			return nil
		}
		return fmt.Errorf("decode rich message: %w", err)
	}

	var decoded RichMessage
	if raw, ok := obj["is_rtl"]; ok && !decodeJSONValue(raw, &decoded.IsRTL) {
		decoded.Malformed = true
	}
	rawBlocks, ok := obj["blocks"]
	switch {
	case !ok:
		decoded.Malformed = true
	case bytes.Equal(bytes.TrimSpace(rawBlocks), []byte("null")):
		decoded.Malformed = true
	default:
		var blocks []json.RawMessage
		if err := json.Unmarshal(rawBlocks, &blocks); err != nil {
			decoded.Malformed = true
		} else {
			decoded.Blocks = make([]RichBlock, 0, len(blocks))
			for _, raw := range blocks {
				block, blockErr := decodeRichBlock(raw, 1)
				if blockErr != nil {
					return fmt.Errorf("decode rich block: %w", blockErr)
				}
				decoded.Blocks = append(decoded.Blocks, block)
			}
		}
	}
	*m = decoded
	return nil
}

func decodeRichText(data []byte, depth int) (RichText, error) {
	if depth > richDecodeHardDepth {
		return RichText{Kind: RichTextArray, Malformed: true}, nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return RichText{Malformed: true}, nil
	}
	switch trimmed[0] {
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return RichText{}, err
		}
		return RichText{Kind: RichTextPlain, Value: value}, nil
	case '[':
		var rawChildren []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawChildren); err != nil {
			return RichText{}, err
		}
		result := RichText{Kind: RichTextArray, Children: make([]RichText, 0, len(rawChildren))}
		for _, raw := range rawChildren {
			child, err := decodeRichText(raw, depth+1)
			if err != nil {
				return RichText{}, err
			}
			result.Children = append(result.Children, child)
		}
		return result, nil
	case '{':
		return decodeRichTextObject(trimmed, depth)
	default:
		// null, numbers and booleans aren't RichText alternatives. Preserve the
		// surrounding update and let projection classify it as partial/invalid.
		if !json.Valid(trimmed) {
			return RichText{}, fmt.Errorf("invalid RichText JSON")
		}
		return RichText{Malformed: true}, nil
	}
}

func decodeRichTextObject(data []byte, depth int) (RichText, error) {
	obj, err := decodeJSONObject(data)
	if err != nil {
		return RichText{}, err
	}
	typeName, typeOK := decodeJSONString(obj["type"])
	kind := RichTextKind(typeName)
	result := RichText{Kind: kind}
	if !typeOK {
		result.Unknown = true
		result.Malformed = true
		result.Kind = RichTextKind("unknown")
	} else if !knownRichTextKind(kind) {
		result.Unknown = true
		result.Kind = RichTextKind(sanitizeRichDiscriminator(typeName))
	}

	if result.Unknown {
		if raw, ok := obj["text"]; ok {
			child, childErr := decodeRichText(raw, depth+1)
			if childErr != nil {
				return RichText{}, childErr
			}
			result.Text = &child
		}
		return result, nil
	}

	if richTextHasNestedText(kind) {
		child, ok, childErr := decodeOptionalRichText(obj, "text", depth+1)
		if childErr != nil {
			return RichText{}, childErr
		}
		result.Text = child
		result.Malformed = result.Malformed || !ok
	}

	switch kind {
	case RichTextDateTime:
		result.Malformed = !decodeJSONValue(obj["unix_time"], &result.UnixTime) || result.Malformed
		result.Malformed = !decodeJSONValue(obj["date_time_format"], &result.DateTimeFormat) || result.Malformed
	case RichTextTextMention:
		var user User
		if raw, ok := obj["user"]; !ok || !decodeJSONValue(raw, &user) {
			result.Malformed = true
		} else {
			result.User = &user
		}
	case RichTextCustomEmoji:
		result.Malformed = !decodeJSONValue(obj["custom_emoji_id"], &result.CustomEmojiID)
		result.Malformed = !decodeJSONValue(obj["alternative_text"], &result.AlternativeText) || result.Malformed
	case RichTextMathematicalExpression:
		result.Malformed = !decodeJSONValue(obj["expression"], &result.Expression)
	case RichTextURL:
		result.Malformed = !decodeJSONValue(obj["url"], &result.URL) || result.Malformed
	case RichTextEmailAddress:
		result.Malformed = !decodeJSONValue(obj["email_address"], &result.EmailAddress) || result.Malformed
	case RichTextPhoneNumber:
		result.Malformed = !decodeJSONValue(obj["phone_number"], &result.PhoneNumber) || result.Malformed
	case RichTextBankCardNumber:
		result.Malformed = !decodeJSONValue(obj["bank_card_number"], &result.BankCardNumber) || result.Malformed
	case RichTextMention:
		result.Malformed = !decodeJSONValue(obj["username"], &result.Username) || result.Malformed
	case RichTextHashtag:
		result.Malformed = !decodeJSONValue(obj["hashtag"], &result.Hashtag) || result.Malformed
	case RichTextCashtag:
		result.Malformed = !decodeJSONValue(obj["cashtag"], &result.Cashtag) || result.Malformed
	case RichTextBotCommand:
		result.Malformed = !decodeJSONValue(obj["bot_command"], &result.BotCommand) || result.Malformed
	case RichTextAnchor:
		result.Malformed = !decodeJSONValue(obj["name"], &result.Name)
	case RichTextAnchorLink:
		result.Malformed = !decodeJSONValue(obj["anchor_name"], &result.AnchorName) || result.Malformed
	case RichTextReference:
		result.Malformed = !decodeJSONValue(obj["name"], &result.Name) || result.Malformed
	case RichTextReferenceLink:
		result.Malformed = !decodeJSONValue(obj["reference_name"], &result.ReferenceName) || result.Malformed
	}
	return result, nil
}

func knownRichTextKind(kind RichTextKind) bool {
	switch kind {
	case RichTextBold, RichTextItalic, RichTextUnderline, RichTextStrikethrough,
		RichTextSpoiler, RichTextDateTime, RichTextTextMention, RichTextSubscript,
		RichTextSuperscript, RichTextMarked, RichTextCode, RichTextCustomEmoji,
		RichTextMathematicalExpression, RichTextURL, RichTextEmailAddress,
		RichTextPhoneNumber, RichTextBankCardNumber, RichTextMention,
		RichTextHashtag, RichTextCashtag, RichTextBotCommand, RichTextAnchor,
		RichTextAnchorLink, RichTextReference, RichTextReferenceLink:
		return true
	default:
		return false
	}
}

func richTextHasNestedText(kind RichTextKind) bool {
	switch kind {
	case RichTextBold, RichTextItalic, RichTextUnderline, RichTextStrikethrough,
		RichTextSpoiler, RichTextDateTime, RichTextTextMention, RichTextSubscript,
		RichTextSuperscript, RichTextMarked, RichTextCode, RichTextURL,
		RichTextEmailAddress, RichTextPhoneNumber, RichTextBankCardNumber,
		RichTextMention, RichTextHashtag, RichTextCashtag, RichTextBotCommand,
		RichTextAnchorLink, RichTextReference, RichTextReferenceLink:
		return true
	default:
		return false
	}
}

func decodeRichBlock(data []byte, depth int) (RichBlock, error) {
	if depth > richDecodeHardDepth {
		return RichBlock{Type: RichBlockType("truncated"), Unknown: true, Malformed: true}, nil
	}
	obj, err := decodeJSONObject(data)
	if err != nil {
		if json.Valid(data) {
			return RichBlock{Unknown: true, Malformed: true}, nil
		}
		return RichBlock{}, err
	}
	typeName, typeOK := decodeJSONString(obj["type"])
	result := RichBlock{Type: RichBlockType(typeName)}
	if !typeOK {
		result.Type = "unknown"
		result.Unknown = true
		result.Malformed = true
	} else if !knownRichBlockType(result.Type) {
		result.Type = RichBlockType(sanitizeRichDiscriminator(typeName))
		result.Unknown = true
	}

	if result.Unknown {
		if err := decodeUnknownRichBlock(obj, depth, &result); err != nil {
			return RichBlock{}, err
		}
		return result, nil
	}

	var fieldOK bool
	switch result.Type {
	case RichBlockParagraph, RichBlockFooter:
		result.Text, fieldOK, err = decodeOptionalRichText(obj, "text", depth+1)
		result.Malformed = result.Malformed || !fieldOK
	case RichBlockPullquote:
		result.Text, fieldOK, err = decodeOptionalRichText(obj, "text", depth+1)
		result.Malformed = result.Malformed || !fieldOK
		if err == nil {
			result.Credit, _, err = decodeOptionalRichText(obj, "credit", depth+1)
		}
	case RichBlockThinking:
		result.Text, _, err = decodeOptionalRichText(obj, "text", depth+1)
		// Thinking blocks are draft-only and must not occur in received rich
		// messages. Decode their visible text, but classify the payload as
		// anomalous/partial rather than dropping the enclosing Update.
		result.Malformed = true
	case RichBlockHeading:
		result.Text, fieldOK, err = decodeOptionalRichText(obj, "text", depth+1)
		result.Malformed = result.Malformed || !fieldOK || !decodeJSONValue(obj["size"], &result.Size)
	case RichBlockPreformatted:
		result.Text, fieldOK, err = decodeOptionalRichText(obj, "text", depth+1)
		result.Malformed = result.Malformed || !fieldOK
		if raw, ok := obj["language"]; ok && !decodeJSONValue(raw, &result.Language) {
			result.Malformed = true
		}
	case RichBlockDivider:
		// No variant-specific fields.
	case RichBlockMathematicalExpression:
		result.Malformed = !decodeJSONValue(obj["expression"], &result.Expression)
	case RichBlockAnchor:
		result.Malformed = !decodeJSONValue(obj["name"], &result.Name)
	case RichBlockList:
		result.Items, fieldOK, err = decodeRichListItems(obj["items"], depth+1)
		result.Malformed = result.Malformed || !fieldOK
	case RichBlockBlockquote:
		result.Blocks, fieldOK, err = decodeRichBlocks(obj["blocks"], depth+1)
		result.Malformed = result.Malformed || !fieldOK
		if err == nil {
			result.Credit, _, err = decodeOptionalRichText(obj, "credit", depth+1)
		}
	case RichBlockCollage, RichBlockSlideshow:
		result.Blocks, fieldOK, err = decodeRichBlocks(obj["blocks"], depth+1)
		result.Malformed = result.Malformed || !fieldOK
		if err == nil {
			result.Caption, _, err = decodeRichCaption(obj, "caption", depth+1)
		}
	case RichBlockTable:
		result.Cells, fieldOK, err = decodeRichTable(obj["cells"], depth+1)
		result.Malformed = result.Malformed || !fieldOK
		if raw, ok := obj["is_bordered"]; ok && !decodeJSONValue(raw, &result.IsBordered) {
			result.Malformed = true
		}
		if raw, ok := obj["is_striped"]; ok && !decodeJSONValue(raw, &result.IsStriped) {
			result.Malformed = true
		}
		if err == nil {
			result.Text, _, err = decodeOptionalRichText(obj, "caption", depth+1)
		}
	case RichBlockDetails:
		result.Summary, fieldOK, err = decodeOptionalRichText(obj, "summary", depth+1)
		result.Malformed = result.Malformed || !fieldOK
		if err == nil {
			result.Blocks, fieldOK, err = decodeRichBlocks(obj["blocks"], depth+1)
			result.Malformed = result.Malformed || !fieldOK
		}
		if raw, ok := obj["is_open"]; ok && !decodeJSONValue(raw, &result.IsOpen) {
			result.Malformed = true
		}
	case RichBlockMap:
		var location Location
		if raw, ok := obj["location"]; !ok || !decodeJSONValue(raw, &location) {
			result.Malformed = true
		} else {
			result.Location = &location
		}
		result.Malformed = !decodeJSONValue(obj["zoom"], &result.Zoom) || result.Malformed
		result.Malformed = !decodeJSONValue(obj["width"], &result.Width) || result.Malformed
		result.Malformed = !decodeJSONValue(obj["height"], &result.Height) || result.Malformed
		result.Caption, _, err = decodeRichCaption(obj, "caption", depth+1)
	case RichBlockAnimation:
		var animation Animation
		if raw, ok := obj["animation"]; !ok || !decodeJSONValue(raw, &animation) {
			result.Malformed = true
		} else {
			result.Animation = &animation
		}
		result.decodeMediaCommon(obj, depth, &err)
	case RichBlockAudio:
		var audio Audio
		if raw, ok := obj["audio"]; !ok || !decodeJSONValue(raw, &audio) {
			result.Malformed = true
		} else {
			result.Audio = &audio
		}
		result.decodeMediaCommon(obj, depth, &err)
	case RichBlockPhoto:
		if raw, ok := obj["photo"]; !ok || !decodeJSONValue(raw, &result.Photo) {
			result.Malformed = true
		}
		result.decodeMediaCommon(obj, depth, &err)
	case RichBlockVideo:
		var video Video
		if raw, ok := obj["video"]; !ok || !decodeJSONValue(raw, &video) {
			result.Malformed = true
		} else {
			result.Video = &video
		}
		result.decodeMediaCommon(obj, depth, &err)
	case RichBlockVoiceNote:
		var voice Voice
		if raw, ok := obj["voice_note"]; !ok || !decodeJSONValue(raw, &voice) {
			result.Malformed = true
		} else {
			result.VoiceNote = &voice
		}
		result.decodeMediaCommon(obj, depth, &err)
	}
	if err != nil {
		return RichBlock{}, err
	}
	return result, nil
}

func (b *RichBlock) decodeMediaCommon(obj map[string]json.RawMessage, depth int, err *error) {
	if raw, ok := obj["has_spoiler"]; ok && !decodeJSONValue(raw, &b.HasSpoiler) {
		b.Malformed = true
	}
	caption, _, captionErr := decodeRichCaption(obj, "caption", depth+1)
	b.Caption = caption
	*err = captionErr
}

func knownRichBlockType(kind RichBlockType) bool {
	switch kind {
	case RichBlockParagraph, RichBlockHeading, RichBlockPreformatted,
		RichBlockFooter, RichBlockDivider, RichBlockMathematicalExpression,
		RichBlockAnchor, RichBlockList, RichBlockBlockquote, RichBlockPullquote,
		RichBlockCollage, RichBlockSlideshow, RichBlockTable, RichBlockDetails,
		RichBlockMap, RichBlockAnimation, RichBlockAudio, RichBlockPhoto,
		RichBlockVideo, RichBlockVoiceNote, RichBlockThinking:
		return true
	default:
		return false
	}
}

func decodeUnknownRichBlock(obj map[string]json.RawMessage, depth int, block *RichBlock) error {
	var err error
	block.Text, _, err = decodeOptionalRichText(obj, "text", depth+1)
	if err != nil {
		return err
	}
	block.Summary, _, err = decodeOptionalRichText(obj, "summary", depth+1)
	if err != nil {
		return err
	}
	if raw, ok := obj["blocks"]; ok {
		block.Blocks, _, err = decodeRichBlocks(raw, depth+1)
		if err != nil {
			return err
		}
	}
	if raw, ok := obj["items"]; ok {
		block.Items, _, err = decodeRichListItems(raw, depth+1)
		if err != nil {
			return err
		}
	}
	if raw, ok := obj["cells"]; ok {
		block.Cells, _, err = decodeRichTable(raw, depth+1)
		if err != nil {
			return err
		}
	}
	block.Caption, _, err = decodeRichCaption(obj, "caption", depth+1)
	return err
}

func decodeOptionalRichText(obj map[string]json.RawMessage, key string, depth int) (*RichText, bool, error) {
	raw, ok := obj[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	value, err := decodeRichText(raw, depth)
	if err != nil {
		return nil, false, err
	}
	return &value, !value.Malformed, nil
}

func decodeRichBlocks(raw json.RawMessage, depth int) ([]RichBlock, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false, nil
	}
	result := make([]RichBlock, 0, len(values))
	valid := true
	for _, value := range values {
		block, err := decodeRichBlock(value, depth)
		if err != nil {
			return nil, false, err
		}
		valid = valid && !block.Malformed
		result = append(result, block)
	}
	return result, valid, nil
}

func decodeRichListItems(raw json.RawMessage, depth int) ([]RichBlockListItem, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false, nil
	}
	result := make([]RichBlockListItem, 0, len(values))
	valid := true
	for _, value := range values {
		obj, err := decodeJSONObject(value)
		if err != nil {
			// Structurally malformed but valid JSON must not discard the whole
			// Update. Preserve an empty anomalous item for projection metrics.
			result = append(result, RichBlockListItem{Malformed: true})
			valid = false
			continue
		}
		item := RichBlockListItem{}
		rawLabel, hasLabel := obj["label"]
		if !hasLabel || !decodeJSONValue(rawLabel, &item.Label) {
			item.Malformed = true
		}
		blocks, validBlocks, err := decodeRichBlocks(obj["blocks"], depth+1)
		if err != nil {
			return nil, false, err
		}
		item.Blocks = blocks
		item.Malformed = item.Malformed || !validBlocks
		if rawCheckbox, ok := obj["has_checkbox"]; ok && !decodeJSONValue(rawCheckbox, &item.HasCheckbox) {
			item.Malformed = true
		}
		if rawChecked, ok := obj["is_checked"]; ok && !decodeJSONValue(rawChecked, &item.IsChecked) {
			item.Malformed = true
		}
		if rawValue, ok := obj["value"]; ok {
			var number int
			if !decodeJSONValue(rawValue, &number) {
				item.Malformed = true
			} else {
				item.Value = &number
			}
		}
		if rawType, ok := obj["type"]; ok {
			if !decodeJSONValue(rawType, &item.Type) || !validRichListLabelType(item.Type) {
				item.Malformed = true
			}
		}
		valid = valid && !item.Malformed
		result = append(result, item)
	}
	return result, valid, nil
}

func validRichListLabelType(value string) bool {
	switch value {
	case "a", "A", "i", "I", "1":
		return true
	default:
		return false
	}
}

func decodeRichCaption(obj map[string]json.RawMessage, key string, depth int) (*RichBlockCaption, bool, error) {
	raw, ok := obj[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	captionObj, err := decodeJSONObject(raw)
	if err != nil {
		return nil, false, nil
	}
	caption := &RichBlockCaption{}
	var textOK bool
	caption.Text, textOK, err = decodeOptionalRichText(captionObj, "text", depth+1)
	if err != nil {
		return nil, false, err
	}
	creditOK := true
	caption.Credit, _, err = decodeOptionalRichText(captionObj, "credit", depth+1)
	if err != nil {
		return nil, false, err
	}
	if caption.Credit != nil && caption.Credit.Malformed {
		creditOK = false
	}
	if !textOK || !creditOK {
		caption.Malformed = true
	}
	return caption, !caption.Malformed, nil
}

func decodeRichTable(raw json.RawMessage, depth int) ([][]RichBlockTableCell, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	var rawRows []json.RawMessage
	if err := json.Unmarshal(raw, &rawRows); err != nil {
		return nil, false, nil
	}
	rows := make([][]RichBlockTableCell, 0, len(rawRows))
	valid := true
	for _, rawRow := range rawRows {
		if bytes.Equal(bytes.TrimSpace(rawRow), []byte("null")) {
			valid = false
			continue
		}
		var rawCells []json.RawMessage
		if err := json.Unmarshal(rawRow, &rawCells); err != nil {
			valid = false
			continue
		}
		row := make([]RichBlockTableCell, 0, len(rawCells))
		for _, rawCell := range rawCells {
			obj, err := decodeJSONObject(rawCell)
			if err != nil {
				row = append(row, RichBlockTableCell{Malformed: true})
				valid = false
				continue
			}
			cell := RichBlockTableCell{}
			cell.Text, _, err = decodeOptionalRichText(obj, "text", depth+1)
			if err != nil {
				return nil, false, err
			}
			for key, destination := range map[string]*int{"colspan": &cell.Colspan, "rowspan": &cell.Rowspan} {
				if rawValue, ok := obj[key]; ok && !decodeJSONValue(rawValue, destination) {
					cell.Malformed = true
				}
			}
			if rawHeader, ok := obj["is_header"]; ok && !decodeJSONValue(rawHeader, &cell.IsHeader) {
				cell.Malformed = true
			}
			if rawAlign, ok := obj["align"]; !ok || !decodeJSONValue(rawAlign, &cell.Align) || !validRichTableAlign(cell.Align) {
				cell.Malformed = true
				cell.Align = ""
			}
			if rawVAlign, ok := obj["valign"]; !ok || !decodeJSONValue(rawVAlign, &cell.VAlign) || !validRichTableVAlign(cell.VAlign) {
				cell.Malformed = true
				cell.VAlign = ""
			}
			valid = valid && !cell.Malformed
			row = append(row, cell)
		}
		rows = append(rows, row)
	}
	return rows, valid, nil
}

func validRichTableAlign(value string) bool {
	return value == "left" || value == "center" || value == "right"
}

func validRichTableVAlign(value string) bool {
	return value == "top" || value == "middle" || value == "bottom"
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return obj, nil
}

func decodeJSONString(raw json.RawMessage) (string, bool) {
	var value string
	ok := decodeJSONValue(raw, &value)
	return value, ok
}

func decodeJSONValue(raw json.RawMessage, destination any) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, destination) == nil
}

func sanitizeRichDiscriminator(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	for _, r := range value {
		runeBytes := utf8.RuneLen(r)
		if runeBytes < 0 {
			runeBytes = 1
		}
		if out.Len()+runeBytes > 64 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}
