package telegram

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRichTextUnmarshalAllOfficialVariants(t *testing.T) {
	t.Parallel()

	data := `[
		{"type":"bold","text":"bold"},
		{"type":"italic","text":"italic"},
		{"type":"underline","text":"underline"},
		{"type":"strikethrough","text":"strike"},
		{"type":"spoiler","text":"spoiler"},
		{"type":"date_time","text":"date","unix_time":1700000000,"date_time_format":"yyyy-MM-dd"},
		{"type":"text_mention","text":"Alice","user":{"id":900719925474099,"is_bot":false,"first_name":"Alice"}},
		{"type":"subscript","text":"sub"},
		{"type":"superscript","text":"super"},
		{"type":"marked","text":"marked"},
		{"type":"code","text":"code"},
		{"type":"custom_emoji","custom_emoji_id":"emoji-1","alternative_text":"🙂"},
		{"type":"mathematical_expression","expression":"x^2+y^2"},
		{"type":"url","text":"site","url":"https://example.com/?a=1&b=2"},
		{"type":"email_address","text":"mail","email_address":"a@example.com"},
		{"type":"phone_number","text":"phone","phone_number":"+12025550123"},
		{"type":"bank_card_number","text":"card","bank_card_number":"4242424242424242"},
		{"type":"mention","text":"user","username":"alice"},
		{"type":"hashtag","text":"topic","hashtag":"richmessages"},
		{"type":"cashtag","text":"ticker","cashtag":"TG"},
		{"type":"bot_command","text":"command","bot_command":"start"},
		{"type":"anchor","name":"top"},
		{"type":"anchor_link","text":"up","anchor_name":"top"},
		{"type":"reference","text":"footnote body","name":"note-1"},
		{"type":"reference_link","text":"[1]","reference_name":"note-1"}
	]`

	var text RichText
	if err := json.Unmarshal([]byte(data), &text); err != nil {
		t.Fatalf("unmarshal RichText: %v", err)
	}
	if text.Kind != RichTextArray {
		t.Fatalf("kind = %q, want %q", text.Kind, RichTextArray)
	}
	wantKinds := []RichTextKind{
		RichTextBold, RichTextItalic, RichTextUnderline, RichTextStrikethrough,
		RichTextSpoiler, RichTextDateTime, RichTextTextMention, RichTextSubscript,
		RichTextSuperscript, RichTextMarked, RichTextCode, RichTextCustomEmoji,
		RichTextMathematicalExpression, RichTextURL, RichTextEmailAddress,
		RichTextPhoneNumber, RichTextBankCardNumber, RichTextMention,
		RichTextHashtag, RichTextCashtag, RichTextBotCommand, RichTextAnchor,
		RichTextAnchorLink, RichTextReference, RichTextReferenceLink,
	}
	if len(text.Children) != len(wantKinds) {
		t.Fatalf("children = %d, want %d", len(text.Children), len(wantKinds))
	}
	for i, want := range wantKinds {
		if got := text.Children[i].Kind; got != want {
			t.Errorf("child %d kind = %q, want %q", i, got, want)
		}
		if text.Children[i].Malformed || text.Children[i].Unknown {
			t.Errorf("child %d (%s) unexpectedly malformed/unknown", i, want)
		}
	}
	if got := text.Children[5].UnixTime; got != 1700000000 {
		t.Errorf("date unix_time = %d", got)
	}
	if got := text.Children[6].User; got == nil || got.ID != 900719925474099 {
		t.Errorf("text mention user = %#v", got)
	}
	if got := text.Children[11].AlternativeText; got != "🙂" {
		t.Errorf("custom emoji alternative = %q", got)
	}
	if got := text.Children[13].URL; got != "https://example.com/?a=1&b=2" {
		t.Errorf("url = %q", got)
	}
	if got := text.Children[21].Name; got != "top" {
		t.Errorf("anchor name = %q", got)
	}
	if got := text.Children[24].ReferenceName; got != "note-1" {
		t.Errorf("reference name = %q", got)
	}
}

func TestMessageUnmarshalAllOfficialRichBlocks(t *testing.T) {
	t.Parallel()

	const largeFile = 5_000_000_000
	data := fmt.Sprintf(`{
		"update_id":7,
		"message":{"message_id":11,"chat":{"id":12,"type":"private"},"date":1700000000,
		"rich_message":{"is_rtl":true,"blocks":[
			{"type":"paragraph","text":"paragraph"},
			{"type":"heading","text":"heading","size":2},
			{"type":"pre","text":"fmt.Println()","language":"go"},
			{"type":"footer","text":"footer"},
			{"type":"divider"},
			{"type":"mathematical_expression","expression":"E=mc^2"},
			{"type":"anchor","name":"chapter-1"},
			{"type":"list","items":[{"label":"3.","blocks":[{"type":"paragraph","text":"item"}],"has_checkbox":true,"is_checked":true,"value":3,"type":"1"}]},
			{"type":"blockquote","blocks":[{"type":"paragraph","text":"quoted"}],"credit":"Author"},
			{"type":"pullquote","text":"pulled","credit":"Pull Author"},
			{"type":"collage","blocks":[{"type":"photo","photo":[{"file_id":"p","file_unique_id":"pu","width":1,"height":2,"file_size":%[1]d}]}],"caption":{"text":"collage caption","credit":"collage credit"}},
			{"type":"slideshow","blocks":[{"type":"video","video":{"file_id":"v","file_unique_id":"vu","width":3,"height":4,"duration":5,"file_size":%[1]d}}],"caption":{"text":"slides"}},
			{"type":"table","cells":[[{"text":"H","is_header":true,"colspan":2,"rowspan":3,"align":"center","valign":"middle"}]],"is_bordered":true,"is_striped":true,"caption":"table caption"},
			{"type":"details","summary":"summary","blocks":[{"type":"paragraph","text":"details body"}],"is_open":true},
			{"type":"map","location":{"latitude":55.75,"longitude":37.62,"horizontal_accuracy":1.5},"zoom":15,"width":640,"height":360,"caption":{"text":"Moscow","credit":"Map data"}},
			{"type":"animation","animation":{"file_id":"a","file_unique_id":"au","width":6,"height":7,"duration":8,"file_size":%[1]d},"has_spoiler":true,"caption":{"text":"animation"}},
			{"type":"audio","audio":{"file_id":"m","file_unique_id":"mu","duration":9,"performer":"Artist","title":"Song","file_size":%[1]d,"thumbnail":{"file_id":"at","file_unique_id":"atu","width":2,"height":3,"file_size":%[1]d}},"caption":{"text":"audio"}},
			{"type":"photo","photo":[{"file_id":"p2","file_unique_id":"p2u","width":10,"height":11,"file_size":%[1]d}],"has_spoiler":true,"caption":{"text":"photo"}},
			{"type":"video","video":{"file_id":"v2","file_unique_id":"v2u","width":12,"height":13,"duration":14,"cover":[{"file_id":"c","file_unique_id":"cu","width":1,"height":1,"file_size":%[1]d}],"start_timestamp":2,"qualities":[{"file_id":"vq","file_unique_id":"vqu","width":1920,"height":1080,"codec":"h265","file_size":%[1]d}],"file_size":%[1]d},"has_spoiler":true,"caption":{"text":"video"}},
			{"type":"voice_note","voice_note":{"file_id":"o","file_unique_id":"ou","duration":15,"file_size":%[1]d},"caption":{"text":"voice"}},
			{"type":"thinking","text":"draft-only"}
		]}}
	}`, largeFile)

	var update Update
	if err := json.Unmarshal([]byte(data), &update); err != nil {
		t.Fatalf("unmarshal Update: %v", err)
	}
	if update.Message == nil || update.Message.RichMessage == nil {
		t.Fatal("rich_message wasn't decoded")
	}
	message := update.Message.RichMessage
	if !message.IsRTL {
		t.Error("is_rtl wasn't decoded")
	}
	wantTypes := []RichBlockType{
		RichBlockParagraph, RichBlockHeading, RichBlockPreformatted, RichBlockFooter,
		RichBlockDivider, RichBlockMathematicalExpression, RichBlockAnchor, RichBlockList,
		RichBlockBlockquote, RichBlockPullquote, RichBlockCollage, RichBlockSlideshow,
		RichBlockTable, RichBlockDetails, RichBlockMap, RichBlockAnimation,
		RichBlockAudio, RichBlockPhoto, RichBlockVideo, RichBlockVoiceNote,
		RichBlockThinking,
	}
	if len(message.Blocks) != len(wantTypes) {
		t.Fatalf("blocks = %d, want %d", len(message.Blocks), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got := message.Blocks[i].Type; got != want {
			t.Errorf("block %d type = %q, want %q", i, got, want)
		}
		if i != len(wantTypes)-1 && message.Blocks[i].Malformed {
			t.Errorf("block %d (%s) unexpectedly malformed", i, want)
		}
	}
	if got := message.Blocks[9].Credit; got == nil || got.Value != "Pull Author" {
		t.Errorf("pullquote credit = %#v", got)
	}
	if got := message.Blocks[7].Items[0].Type; got != "1" {
		t.Errorf("list item type = %q", got)
	}
	if !message.Blocks[20].Malformed {
		t.Error("received draft-only thinking block must be anomalous")
	}
	cell := message.Blocks[12].Cells[0][0]
	if cell.Colspan != 2 || cell.Rowspan != 3 || cell.Align != "center" || cell.VAlign != "middle" {
		t.Errorf("table cell = %#v", cell)
	}
	if got := message.Blocks[14].Location; got == nil || got.Latitude != 55.75 || got.HorizontalAccuracy != 1.5 {
		t.Errorf("map location = %#v", got)
	}
	if got := message.Blocks[15].Animation; got == nil || got.FileSize != largeFile {
		t.Errorf("animation = %#v", got)
	}
	if got := message.Blocks[16].Audio; got == nil || got.FileSize != largeFile || got.Thumbnail == nil || got.Thumbnail.FileSize != largeFile {
		t.Errorf("audio = %#v", got)
	}
	if got := message.Blocks[17].Photo[0].FileSize; got != largeFile {
		t.Errorf("photo file_size = %d", got)
	}
	if got := message.Blocks[18].Video; got == nil || got.FileSize != largeFile || got.Cover[0].FileSize != largeFile || len(got.Qualities) != 1 || got.Qualities[0].Codec != "h265" || got.Qualities[0].FileSize != largeFile {
		t.Errorf("video = %#v", got)
	}
	if got := message.Blocks[19].VoiceNote; got == nil || got.FileSize != largeFile {
		t.Errorf("voice = %#v", got)
	}
}

func TestTelegramFileSizesDecodeBeyondInt32(t *testing.T) {
	t.Parallel()

	const data = `{"file_id":"f","file_unique_id":"u","file_size":5000000000}`
	for name, destination := range map[string]any{
		"photo":         new(PhotoSize),
		"document":      new(Document),
		"voice":         new(Voice),
		"audio":         new(Audio),
		"animation":     new(Animation),
		"video":         new(Video),
		"video_quality": new(VideoQuality),
		"video_note":    new(VideoNote),
		"file":          new(File),
	} {
		destination := destination
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(data), destination); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			encoded, err := json.Marshal(destination)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(encoded), `"file_size":5000000000`) {
				t.Fatalf("large file_size lost: %s", encoded)
			}
		})
	}
}

func TestRichDecoderUnknownVariantsAreTolerantAndDoNotRetainRawJSON(t *testing.T) {
	t.Parallel()

	const secret = "DO_NOT_PROJECT_ME"
	data := `{"update_id":1,"message":{"message_id":2,"chat":{"id":3,"type":"private"},"date":4,"rich_message":{"blocks":[
		{"type":"future<script>","text":{"type":"future_inline","text":"safe text","secret":"` + secret + `"},"summary":"safe summary","blocks":[{"type":"paragraph","text":"safe child"}],"items":[{"label":"*","blocks":[{"type":"paragraph","text":"safe item"}]}],"cells":[[{"text":"safe cell"}]],"caption":{"text":"safe caption"},"secret":"` + secret + `"}
	]}}}`

	var update Update
	if err := json.Unmarshal([]byte(data), &update); err != nil {
		t.Fatalf("unknown variants must not reject an Update: %v", err)
	}
	block := update.Message.RichMessage.Blocks[0]
	if !block.Unknown || block.Type != "future_script_" {
		t.Fatalf("unknown block = %#v", block)
	}
	if block.Text == nil || !block.Text.Unknown || block.Text.Text == nil || block.Text.Text.Value != "safe text" {
		t.Fatalf("safe unknown text wasn't salvaged: %#v", block.Text)
	}
	if len(block.Blocks) != 1 || len(block.Items) != 1 || len(block.Cells) != 1 || block.Caption == nil {
		t.Fatalf("safe unknown shapes weren't salvaged: %#v", block)
	}
	projected, err := ProjectRichMessage(update.Message.RichMessage)
	if err != nil {
		t.Fatalf("project unknown message: %v", err)
	}
	if projected.Disposition != RichDispositionPartial {
		t.Errorf("disposition = %q, want partial", projected.Disposition)
	}
	if strings.Contains(projected.Markdown, secret) || strings.Contains(strings.Join(projected.UnknownKinds, " "), secret) {
		t.Fatalf("unknown raw data leaked: %#v", projected)
	}
	for _, safe := range []string{"safe text", "safe summary", "safe child", "safe item", "safe cell", "safe caption"} {
		if !strings.Contains(projected.Markdown, safe) {
			t.Errorf("salvaged %q missing from %q", safe, projected.Markdown)
		}
	}
}

func TestRichDecoderMalformedFieldsRemainTolerant(t *testing.T) {
	t.Parallel()

	data := `{"blocks":[
		{"type":"heading","text":42,"size":"large"},
		{"type":"list","items":[42,null,{"blocks":[]}]},
		{"type":"table","cells":[[42,null,{"text":"missing required alignment"}]]}
	]}`
	var message RichMessage
	if err := json.Unmarshal([]byte(data), &message); err != nil {
		t.Fatalf("malformed known fields must be represented, not reject JSON: %v", err)
	}
	if len(message.Blocks) != 3 || !message.Blocks[0].Malformed || !message.Blocks[1].Malformed || !message.Blocks[2].Malformed {
		t.Fatalf("malformed flags = %#v", message.Blocks)
	}
	projected, err := ProjectRichMessage(&message)
	if err != nil {
		t.Fatalf("project malformed payload: %v", err)
	}
	if projected.Disposition != RichDispositionPartial {
		t.Errorf("disposition = %q, want partial", projected.Disposition)
	}
}

func TestRichMessageValidNonObjectIsTolerated(t *testing.T) {
	t.Parallel()

	for _, input := range []string{`null`, `[]`, `"wrong"`, `42`} {
		var message RichMessage
		if err := json.Unmarshal([]byte(input), &message); err != nil {
			t.Errorf("unmarshal %s: %v", input, err)
			continue
		}
		if !message.Malformed {
			t.Errorf("unmarshal %s didn't mark malformed", input)
		}
	}
}

func TestRichDecoderNullContainersAreMalformedButTolerated(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"message blocks": `{"blocks":null}`,
		"list items":     `{"blocks":[{"type":"list","items":null}]}`,
		"table cells":    `{"blocks":[{"type":"table","cells":[null]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var message RichMessage
			if err := json.Unmarshal([]byte(input), &message); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !message.Malformed && (len(message.Blocks) == 0 || !message.Blocks[0].Malformed) {
				t.Fatalf("payload wasn't marked malformed: %#v", message)
			}
		})
	}
}

func TestRichDecoderHardRecursionBound(t *testing.T) {
	t.Parallel()

	nestedText := `"leaf"`
	for i := 0; i < 128; i++ {
		nestedText = `{"type":"bold","text":` + nestedText + `}`
	}
	var text RichText
	if err := json.Unmarshal([]byte(nestedText), &text); err != nil {
		t.Fatalf("deep RichText unmarshal: %v", err)
	}
	if !containsMalformedRichText(&text) {
		t.Fatal("deep RichText wasn't truncated/marked malformed")
	}

	nestedBlock := `{"type":"paragraph","text":"leaf"}`
	for i := 0; i < 128; i++ {
		nestedBlock = `{"type":"details","summary":"s","blocks":[` + nestedBlock + `]}`
	}
	var block RichBlock
	if err := json.Unmarshal([]byte(nestedBlock), &block); err != nil {
		t.Fatalf("deep RichBlock unmarshal: %v", err)
	}
	if !containsMalformedRichBlock(&block) {
		t.Fatal("deep RichBlock wasn't truncated/marked malformed")
	}
}

func containsMalformedRichText(text *RichText) bool {
	if text == nil {
		return false
	}
	if text.Malformed {
		return true
	}
	if containsMalformedRichText(text.Text) {
		return true
	}
	for i := range text.Children {
		if containsMalformedRichText(&text.Children[i]) {
			return true
		}
	}
	return false
}

func containsMalformedRichBlock(block *RichBlock) bool {
	if block == nil {
		return false
	}
	if block.Malformed {
		return true
	}
	for i := range block.Blocks {
		if containsMalformedRichBlock(&block.Blocks[i]) {
			return true
		}
	}
	return false
}
