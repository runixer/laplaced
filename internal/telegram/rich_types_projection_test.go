package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectRichTextAllOfficialVariants(t *testing.T) {
	t.Parallel()

	const data = `{"blocks":[{"type":"paragraph","text":[
		{"type":"bold","text":"bold"}," ",
		{"type":"italic","text":"italic"}," ",
		{"type":"underline","text":"underline"}," ",
		{"type":"strikethrough","text":"strike"}," ",
		{"type":"spoiler","text":"spoiler"}," ",
		{"type":"date_time","text":"date","unix_time":1700000000,"date_time_format":"yyyy-MM-dd"}," ",
		{"type":"text_mention","text":"Alice","user":{"id":42,"is_bot":false,"first_name":"Alice"}}," ",
		{"type":"subscript","text":"sub"}," ",
		{"type":"superscript","text":"super"}," ",
		{"type":"marked","text":"marked"}," ",
		{"type":"code","text":"a\u0060b"}," ",
		{"type":"custom_emoji","custom_emoji_id":"emoji-1","alternative_text":"🙂"}," ",
		{"type":"mathematical_expression","expression":"x^2+y^2"}," ",
		{"type":"url","text":"site","url":"https://example.com/?a=1&b=2"}," ",
		{"type":"email_address","text":"mail","email_address":"a@example.com"}," ",
		{"type":"phone_number","text":"phone","phone_number":"+12025550123"}," ",
		{"type":"bank_card_number","text":"card","bank_card_number":"4242"}," ",
		{"type":"mention","text":"user","username":"alice"}," ",
		{"type":"hashtag","text":"topic","hashtag":"rich"}," ",
		{"type":"cashtag","text":"ticker","cashtag":"TG"}," ",
		{"type":"bot_command","text":"command","bot_command":"start"}," ",
		{"type":"anchor","name":"top"},
		{"type":"anchor_link","text":"up","anchor_name":"top"}," ",
		{"type":"reference","text":"footnote body","name":"note-1"}," ",
		{"type":"reference_link","text":"[1]","reference_name":"note-1"}
	]}]}`

	var message RichMessage
	if err := json.Unmarshal([]byte(data), &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := ProjectRichMessage(&message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionAccepted {
		t.Fatalf("disposition = %q", got.Disposition)
	}
	if !got.HasVisibleText {
		t.Error("text projection must report semantic visible text")
	}
	for _, want := range []string{
		"**bold**", "*italic*", "underline", "~~strike~~", "||spoiler||",
		"date [time: unix=1700000000, format=yyyy\\-MM\\-dd]",
		"Alice [Telegram user id=42]", "sub", "super", "marked",
		"`` a`b ``", "🙂", "$x^2+y^2$",
		"site (URL: https://example\\.com/?a=1&b=2)",
		"mail", "phone", "card", "user [username: alice]", "topic [hashtag: rich]",
		"ticker [cashtag: TG]", "command [command: start]",
		"up [anchor: top]", "footnote body [reference: note\\-1]",
		"\\[1\\] [reference: note\\-1]",
	} {
		if !strings.Contains(got.Markdown, want) {
			t.Errorf("projection missing %q:\n%s", want, got.Markdown)
		}
	}
	if strings.Contains(got.Markdown, "anchor name") {
		t.Errorf("anchor emitted positional junk: %q", got.Markdown)
	}
	for _, hidden := range []string{"a@example.com", "+12025550123", "4242"} {
		if strings.Contains(got.Markdown, hidden) {
			t.Errorf("projection exposed hidden typed target %q: %q", hidden, got.Markdown)
		}
	}
	if got.Stats.Characters == 0 || got.Stats.Unknown != 0 {
		t.Errorf("stats = %#v", got.Stats)
	}
}

func TestProjectRichTextSensitiveTargetsUseOnlyVisibleLabels(t *testing.T) {
	t.Parallel()

	const data = `{"blocks":[{"type":"paragraph","text":[
		{"type":"email_address","text":"masked mail","email_address":"secret@example.com"}," ",
		{"type":"phone_number","text":"masked phone","phone_number":"+19995550123"}," ",
		{"type":"bank_card_number","text":"•••• 4242","bank_card_number":"4111111111111111"}," ",
		{"type":"email_address","email_address":"hidden@example.com"}," ",
		{"type":"phone_number","phone_number":"+18885550123"}," ",
		{"type":"bank_card_number","bank_card_number":"5555555555554444"}
	]}]}`

	var message RichMessage
	require.NoError(t, json.Unmarshal([]byte(data), &message))
	got, err := ProjectRichMessage(&message)
	require.NoError(t, err)

	assert.Contains(t, got.Markdown, "masked mail masked phone •••• 4242")
	assert.Contains(t, got.Markdown, "[email address] [phone number] [bank card]")
	for _, hidden := range []string{
		"secret@example.com", "+19995550123", "4111111111111111",
		"hidden@example.com", "+18885550123", "5555555555554444",
	} {
		assert.NotContains(t, got.Markdown, hidden)
	}
	assert.Equal(t, RichDispositionPartial, got.Disposition, "missing visible labels are tolerated but structurally partial")
	assert.True(t, got.HasVisibleText)
}

func TestProjectRichBlocksSemanticMarkdown(t *testing.T) {
	t.Parallel()

	const data = `{"blocks":[
		{"type":"heading","text":"Title","size":2},
		{"type":"pre","text":"line \u0060\u0060\u0060 inside","language":"go"},
		{"type":"footer","text":"footer"},
		{"type":"divider"},
		{"type":"mathematical_expression","expression":"x$y"},
		{"type":"anchor","name":"invisible"},
		{"type":"list","items":[
			{"label":"•","blocks":[{"type":"paragraph","text":"first"}]},
			{"label":"C.","value":3,"type":"A","blocks":[{"type":"paragraph","text":"third"}]},
			{"label":"task","has_checkbox":true,"is_checked":true,"blocks":[{"type":"paragraph","text":"done"}]}
		]},
		{"type":"blockquote","blocks":[{"type":"paragraph","text":"quoted"}],"credit":"Writer"},
		{"type":"pullquote","text":"pulled","credit":"Speaker"},
		{"type":"details","summary":"More","blocks":[{"type":"paragraph","text":"body"}],"is_open":true},
		{"type":"map","location":{"latitude":55.75,"longitude":37.62},"zoom":15,"width":640,"height":360,"caption":{"text":"Moscow","credit":"OSM"}}
	]}`
	var message RichMessage
	if err := json.Unmarshal([]byte(data), &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := ProjectRichMessage(&message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionAccepted {
		t.Fatalf("disposition = %q", got.Disposition)
	}
	for _, want := range []string{
		"## Title", "````go\nline ``` inside\n````", "Footer: footer", "---",
		"$$\nx\\$y\n$$", "- first", "C\\. third", "- [x] done",
		"> quoted\n> \n> — Writer", "> pulled\n> \n> — Speaker",
		"**Details (open):** More\n\nbody",
		"[Map: latitude=55.75, longitude=37.62, zoom=15, width=640, height=360]",
		"Caption: Moscow", "Credit: OSM",
	} {
		if !strings.Contains(got.Markdown, want) {
			t.Errorf("projection missing %q:\n%s", want, got.Markdown)
		}
	}
	if strings.Contains(got.Markdown, "invisible") {
		t.Errorf("block anchor emitted positional junk: %q", got.Markdown)
	}
}

func TestProjectMapOnlyIsAcceptedAndKeepsOfficialMetadata(t *testing.T) {
	t.Parallel()

	message := &RichMessage{Blocks: []RichBlock{{
		Type:     RichBlockMap,
		Location: &Location{Latitude: -33.8688, Longitude: 151.2093},
		Zoom:     17,
		Width:    800,
		Height:   600,
	}}}
	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionAccepted {
		t.Fatalf("disposition = %q, markdown = %q", got.Disposition, got.Markdown)
	}
	if !got.HasVisibleText {
		t.Error("map-only projection must report semantic visible text")
	}
	want := "[Map: latitude=-33.8688, longitude=151.2093, zoom=17, width=800, height=600]"
	if got.Markdown != want {
		t.Errorf("markdown = %q, want %q", got.Markdown, want)
	}
	if got.Stats.Characters != 0 {
		t.Errorf("map metadata consumed source characters: %d", got.Stats.Characters)
	}
}

func TestProjectTablesSimpleAndComplex(t *testing.T) {
	t.Parallel()

	plain := func(value string) *RichText { return &RichText{Kind: RichTextPlain, Value: value} }
	simple := &RichMessage{Blocks: []RichBlock{{
		Type: RichBlockTable,
		Text: plain("Prices"),
		Cells: [][]RichBlockTableCell{
			{{Text: plain("Left"), IsHeader: true, Align: "left", VAlign: "top"}, {Text: plain("Center"), IsHeader: true, Align: "center", VAlign: "top"}, {Text: plain("Right"), IsHeader: true, Align: "right", VAlign: "top"}},
			{{Text: plain("a|b"), Align: "left", VAlign: "top"}, {Text: plain("middle"), Align: "center", VAlign: "top"}, {Text: plain("$12.34"), Align: "right", VAlign: "top"}},
		},
	}}}
	got, err := ProjectRichMessage(simple)
	if err != nil {
		t.Fatalf("simple table: %v", err)
	}
	want := "Table: Prices\n\n| Left | Center | Right |\n| --- | :---: | ---: |\n| a\\|b | middle | \\$12\\.34 |"
	if got.Markdown != want {
		t.Errorf("simple table:\n%s\nwant:\n%s", got.Markdown, want)
	}

	complex := &RichMessage{Blocks: []RichBlock{{
		Type: RichBlockTable,
		Cells: [][]RichBlockTableCell{
			{{Text: plain("wide"), IsHeader: true, Colspan: 2, Rowspan: 2, Align: "center", VAlign: "bottom"}},
			{{Text: plain("tail")}, {Text: nil}},
		},
	}}}
	got, err = ProjectRichMessage(complex)
	if err != nil {
		t.Fatalf("complex table: %v", err)
	}
	for _, want := range []string{"Table (row-by-row):", "Cell 1 (header, colspan=2, rowspan=2, align=center, valign=bottom): wide", "Row 2:", "Cell 2:"} {
		if !strings.Contains(got.Markdown, want) {
			t.Errorf("complex table missing %q:\n%s", want, got.Markdown)
		}
	}

	rowspanOverflow := &RichMessage{Blocks: []RichBlock{{
		Type: RichBlockTable,
		Cells: [][]RichBlockTableCell{
			{{Text: plain("held"), Rowspan: 2}},
			make([]RichBlockTableCell, RichMessageMaxTableColumns),
		},
	}}}
	_, err = ProjectRichMessage(rowspanOverflow)
	var validation *RichValidationError
	if !errors.As(err, &validation) || validation.Field != "table_columns" || validation.Actual != RichMessageMaxTableColumns+1 {
		t.Fatalf("rowspan-aware width error = %#v / %v", validation, err)
	}
}

func TestProjectNestedMediaOrderPathsAndContainerMetadata(t *testing.T) {
	t.Parallel()

	caption := func(text, credit string) *RichBlockCaption {
		return &RichBlockCaption{Text: richPlain(text), Credit: richPlain(credit)}
	}
	photo := func(id, captionText string) RichBlock {
		return RichBlock{
			Type: RichBlockPhoto,
			Photo: []PhotoSize{
				{FileID: id + "-small", FileUniqueID: "same-unique", Width: 10, Height: 10},
				{FileID: id + "-large", FileUniqueID: "same-unique", Width: 100, Height: 100},
			},
			Caption: caption(captionText, captionText+" credit"),
		}
	}
	message := &RichMessage{Blocks: []RichBlock{
		{
			Type:    RichBlockCollage,
			Caption: caption("collage", "collage credit"),
			Blocks: []RichBlock{
				photo("p1", "first"),
				{Type: RichBlockDetails, Summary: richPlain("nested"), Blocks: []RichBlock{{
					Type:    RichBlockSlideshow,
					Caption: caption("slides", "slides credit"),
					Blocks: []RichBlock{
						{Type: RichBlockVideo, Video: &Video{FileID: "v", FileUniqueID: "vu"}, Caption: caption("video", "video credit")},
						photo("p2", "second"),
					},
				}}},
			},
		},
		{Type: RichBlockAnimation, Animation: &Animation{FileID: "a", FileUniqueID: "au"}},
		{Type: RichBlockAudio, Audio: &Audio{FileID: "m", FileUniqueID: "mu"}},
		{Type: RichBlockVoiceNote, VoiceNote: &Voice{FileID: "o", FileUniqueID: "ou"}},
	}}

	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionAccepted {
		t.Fatalf("disposition = %q", got.Disposition)
	}
	wantKinds := []RichMediaKind{RichMediaPhoto, RichMediaVideo, RichMediaPhoto, RichMediaAnimation, RichMediaAudio, RichMediaVoice}
	if len(got.Media) != len(wantKinds) {
		t.Fatalf("media = %d, want %d", len(got.Media), len(wantKinds))
	}
	for i, wantKind := range wantKinds {
		media := got.Media[i]
		if media.Ordinal != i+1 || media.Kind != wantKind {
			t.Errorf("media %d = ordinal %d kind %q", i, media.Ordinal, media.Kind)
		}
		wantMarker := fmt.Sprintf("[[telegram-rich-media:%d:%s]]", i+1, wantKind)
		if media.Marker != wantMarker || !strings.Contains(got.Markdown, wantMarker) {
			t.Errorf("media %d marker = %q", i, media.Marker)
		}
	}
	if got.Media[0].BlockPath != "blocks[0].blocks[0]" || got.Media[0].ContainerPath != "blocks[0]" {
		t.Errorf("first paths = block %q container %q", got.Media[0].BlockPath, got.Media[0].ContainerPath)
	}
	if got.Media[0].Caption != "first" || got.Media[0].Credit != "first credit" || got.Media[0].ContainerCaption != "collage" || got.Media[0].ContainerCredit != "collage credit" {
		t.Errorf("first metadata = %#v", got.Media[0])
	}
	if got.Media[1].BlockPath != "blocks[0].blocks[1].blocks[0].blocks[0]" || got.Media[1].ContainerPath != "blocks[0].blocks[1].blocks[0]" {
		t.Errorf("nested paths = block %q container %q", got.Media[1].BlockPath, got.Media[1].ContainerPath)
	}
	if got.Media[1].ContainerCaption != "slides" || got.Media[1].ContainerCredit != "slides credit" {
		t.Errorf("nested container metadata = %#v", got.Media[1])
	}
	if len(got.Media[0].Photo) != 2 || got.Stats.Media != 6 {
		t.Errorf("photo sizes/media count = %d/%d", len(got.Media[0].Photo), got.Stats.Media)
	}
	if got.Media[0].Photo[0].FileUniqueID != got.Media[2].Photo[0].FileUniqueID {
		t.Fatal("test fixture must contain duplicate file_unique_id occurrences")
	}
}

func TestProjectUnknownKindsAreBoundedAndSanitized(t *testing.T) {
	t.Parallel()

	blocks := []RichBlock{{Type: RichBlockParagraph, Text: richPlain("known")}}
	for i := 0; i < 40; i++ {
		blocks = append(blocks, RichBlock{
			Type:    RichBlockType(fmt.Sprintf("future/%d<script>", i)),
			Unknown: true,
		})
	}
	got, err := ProjectRichMessage(&RichMessage{Blocks: blocks})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionPartial {
		t.Errorf("disposition = %q", got.Disposition)
	}
	if len(got.UnknownKinds) != DefaultRichProjectionLimits().UnknownKinds {
		t.Errorf("unknown kinds = %d", len(got.UnknownKinds))
	}
	for _, kind := range got.UnknownKinds {
		if strings.ContainsAny(kind, "</>") || len(kind) > 64 {
			t.Errorf("unsafe unknown kind %q", kind)
		}
	}
}

func TestProjectEscapesUserMarkdownAndPreservesCode(t *testing.T) {
	t.Parallel()

	input := "###SPLIT### *bold?* [link](javascript:alert(1)) | $100 <script>\n---"
	message := &RichMessage{Blocks: []RichBlock{
		{Type: RichBlockParagraph, Text: richPlain(input)},
		{Type: RichBlockPreformatted, Text: richPlain("literal $x$ ``` <tag>\n###SPLIT###"), Language: "go<script>"},
	}}
	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	for _, want := range []string{`\#\#\#SPLIT\#\#\#`, `\*bold?\*`, `\[link\]\(javascript:alert\(1\)\)`, `\|`, `\$100`, `\<script\>`, `\-\-\-`} {
		if !strings.Contains(got.Markdown, want) {
			t.Errorf("escaped projection missing %q:\n%s", want, got.Markdown)
		}
	}
	if !strings.Contains(got.Markdown, "````\nliteral $x$ ``` <tag>\n###SPLIT###\n````") {
		t.Errorf("literal code wasn't preserved/dynamic-fenced:\n%s", got.Markdown)
	}
	if strings.Contains(got.Markdown, "go<script>") {
		t.Error("unsafe fenced-code language was retained")
	}
}

func TestProjectOfficialLimitsExactBoundary(t *testing.T) {
	t.Parallel()

	t.Run("characters", func(t *testing.T) {
		at := &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: richPlain(strings.Repeat("я", RichMessageMaxCharacters))}}}
		assertProjectionBoundary(t, at, "")
		atWithMap := &RichMessage{Blocks: append(append([]RichBlock(nil), at.Blocks...), RichBlock{
			Type: RichBlockMap, Location: &Location{Latitude: 1, Longitude: 2}, Zoom: 15, Width: 10, Height: 10,
		})}
		assertProjectionBoundary(t, atWithMap, "")
		over := &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: richPlain(strings.Repeat("я", RichMessageMaxCharacters+1))}}}
		assertProjectionBoundary(t, over, "characters")
	})

	t.Run("blocks", func(t *testing.T) {
		atBlocks := make([]RichBlock, RichMessageMaxBlocks)
		atBlocks[0] = RichBlock{Type: RichBlockParagraph, Text: richPlain("visible")}
		for i := 1; i < len(atBlocks); i++ {
			atBlocks[i] = RichBlock{Type: RichBlockDivider}
		}
		assertProjectionBoundary(t, &RichMessage{Blocks: atBlocks}, "")
		overBlocks := append(append([]RichBlock(nil), atBlocks...), RichBlock{Type: RichBlockDivider})
		assertProjectionBoundary(t, &RichMessage{Blocks: overBlocks}, "blocks")
	})

	t.Run("nesting_depth", func(t *testing.T) {
		at := &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: nestedRichText(14)}}}
		assertProjectionBoundary(t, at, "")
		over := &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: nestedRichText(15)}}}
		assertProjectionBoundary(t, over, "nesting_depth")
	})

	t.Run("media", func(t *testing.T) {
		makeMedia := func(count int) *RichMessage {
			blocks := make([]RichBlock, count)
			for i := range blocks {
				blocks[i] = RichBlock{Type: RichBlockPhoto, Photo: []PhotoSize{{FileID: fmt.Sprintf("p-%d", i)}}}
			}
			return &RichMessage{Blocks: blocks}
		}
		assertProjectionBoundary(t, makeMedia(RichMessageMaxMedia), "")
		assertProjectionBoundary(t, makeMedia(RichMessageMaxMedia+1), "media")
	})

	t.Run("table_columns", func(t *testing.T) {
		makeTable := func(columns int) *RichMessage {
			cells := make([]RichBlockTableCell, columns)
			for i := range cells {
				cells[i] = RichBlockTableCell{Text: richPlain(fmt.Sprintf("c%d", i)), IsHeader: true}
			}
			return &RichMessage{Blocks: []RichBlock{{Type: RichBlockTable, Cells: [][]RichBlockTableCell{cells}}}}
		}
		assertProjectionBoundary(t, makeTable(RichMessageMaxTableColumns), "")
		assertProjectionBoundary(t, makeTable(RichMessageMaxTableColumns+1), "table_columns")
	})
}

func TestProjectMediaOnlyDoesNotReportMarkerAsVisibleText(t *testing.T) {
	t.Parallel()

	message := &RichMessage{Blocks: []RichBlock{{
		Type:  RichBlockPhoto,
		Photo: []PhotoSize{{FileID: "photo", FileUniqueID: "photo-unique"}},
	}}}
	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionAccepted || len(got.Media) != 1 {
		t.Fatalf("projection = %#v", got)
	}
	if got.HasVisibleText {
		t.Fatalf("media marker was classified as visible text: %q", got.Markdown)
	}
}

func TestProjectSafetyBudgetsExactBoundary(t *testing.T) {
	t.Parallel()

	t.Run("rich_text_nodes", func(t *testing.T) {
		makeMessage := func(anchorCount int) *RichMessage {
			children := []RichText{{Kind: RichTextPlain, Value: "x"}}
			for i := 0; i < anchorCount; i++ {
				children = append(children, RichText{Kind: RichTextAnchor, Name: fmt.Sprintf("a-%d", i)})
			}
			return &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: &RichText{Kind: RichTextArray, Children: children}}}}
		}
		limits := DefaultRichProjectionLimits()
		limits.RichTextNodes = 4 // array + visible text + two anchors
		if got, err := ProjectRichMessageWithLimits(makeMessage(2), limits); err != nil || got.Stats.RichTextNodes != 4 {
			t.Fatalf("at node boundary = (%#v, %v)", got, err)
		}
		got, err := ProjectRichMessageWithLimits(makeMessage(3), limits)
		var validation *RichValidationError
		if !errors.As(err, &validation) || validation.Field != "rich_text_nodes" || validation.Actual != 5 || got.Disposition != RichDispositionInvalid {
			t.Fatalf("over node boundary = (%#v, %#v, %v)", got, validation, err)
		}
	})

	t.Run("output_characters", func(t *testing.T) {
		limits := DefaultRichProjectionLimits()
		limits.OutputCharacters = 10
		makeMessage := func(count int) *RichMessage {
			return &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: richPlain(strings.Repeat("x", count))}}}
		}
		if got, err := ProjectRichMessageWithLimits(makeMessage(10), limits); err != nil || got.Stats.OutputCharacters != 10 {
			t.Fatalf("at output boundary = (%#v, %v)", got, err)
		}
		got, err := ProjectRichMessageWithLimits(makeMessage(11), limits)
		var validation *RichValidationError
		if !errors.As(err, &validation) || validation.Field != "output_characters" || validation.Actual != 11 || got.Disposition != RichDispositionInvalid {
			t.Fatalf("over output boundary = (%#v, %#v, %v)", got, validation, err)
		}
	})
}

func TestProjectNilEmptyAndThinkingDispositions(t *testing.T) {
	t.Parallel()

	if got, err := ProjectRichMessage(nil); err == nil || got.Disposition != RichDispositionInvalid {
		t.Fatalf("nil = (%#v, %v)", got, err)
	}
	if got, err := ProjectRichMessage(&RichMessage{}); err != nil || got.Disposition != RichDispositionUnsupported {
		t.Fatalf("empty = (%#v, %v)", got, err)
	}
	thinking := &RichMessage{Blocks: []RichBlock{{Type: RichBlockThinking, Text: richPlain("working")}}}
	if got, err := ProjectRichMessage(thinking); err != nil || got.Disposition != RichDispositionPartial || !strings.Contains(got.Markdown, "working") {
		t.Fatalf("thinking = (%#v, %v)", got, err)
	}
}

func TestProjectMalformedMediaPreservesOccurrence(t *testing.T) {
	t.Parallel()

	message := &RichMessage{Blocks: []RichBlock{{Type: RichBlockVideo, Malformed: true}}}
	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Disposition != RichDispositionPartial || len(got.Media) != 1 {
		t.Fatalf("projection = %#v", got)
	}
	if got.Media[0].Kind != RichMediaVideo || got.Media[0].Video != nil || got.Media[0].BlockPath != "blocks[0]" {
		t.Errorf("malformed occurrence = %#v", got.Media[0])
	}
	if !strings.Contains(got.Markdown, got.Media[0].Marker) {
		t.Errorf("marker %q missing from %q", got.Media[0].Marker, got.Markdown)
	}
}

func FuzzRichMessageUnmarshalAndProject(f *testing.F) {
	for _, seed := range []string{
		`{"blocks":[{"type":"paragraph","text":"hello"}]}`,
		`{"blocks":[{"type":"future","text":"safe","opaque":{"x":1}}]}`,
		`{"blocks":[{"type":"details","summary":"s","blocks":[]}]}`,
		`null`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		var message RichMessage
		if err := json.Unmarshal([]byte(input), &message); err != nil {
			return
		}
		_, _ = ProjectRichMessage(&message)
	})
}

func richPlain(value string) *RichText {
	return &RichText{Kind: RichTextPlain, Value: value}
}

func nestedRichText(wrappers int) *RichText {
	text := richPlain("x")
	for i := 0; i < wrappers; i++ {
		text = &RichText{Kind: RichTextBold, Text: text}
	}
	return text
}

func assertProjectionBoundary(t *testing.T, message *RichMessage, wantField string) {
	t.Helper()
	got, err := ProjectRichMessage(message)
	if wantField == "" {
		if err != nil {
			t.Fatalf("at boundary: %v (stats %#v)", err, got.Stats)
		}
		if got.Disposition == RichDispositionInvalid {
			t.Fatalf("at boundary disposition = invalid (stats %#v)", got.Stats)
		}
		return
	}
	if err == nil {
		t.Fatalf("over %s boundary unexpectedly succeeded: %#v", wantField, got.Stats)
	}
	var validation *RichValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("over %s error = %T %v", wantField, err, err)
	}
	if validation.Field != wantField || validation.Actual != validation.Limit+1 {
		t.Fatalf("validation = %#v, want field %s and N+1", validation, wantField)
	}
}

func TestProjectionDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	message := &RichMessage{Blocks: []RichBlock{{
		Type:  RichBlockPhoto,
		Photo: []PhotoSize{{FileID: "p", FileUniqueID: "u", Width: 1, Height: 2}},
		Caption: &RichBlockCaption{
			Text: richPlain("caption"),
		},
	}}}
	before := *message
	before.Blocks = append([]RichBlock(nil), message.Blocks...)
	before.Blocks[0].Photo = append([]PhotoSize(nil), message.Blocks[0].Photo...)
	if _, err := ProjectRichMessage(message); err != nil {
		t.Fatalf("project: %v", err)
	}
	if !reflect.DeepEqual(message, &before) {
		t.Fatalf("input mutated:\n got %#v\nwant %#v", message, &before)
	}
}

func TestProjectionMediaDoesNotAliasInput(t *testing.T) {
	t.Parallel()

	video := &Video{
		FileID:    "video",
		Thumbnail: &PhotoSize{FileID: "thumb"},
		Cover:     []PhotoSize{{FileID: "cover"}},
		Qualities: []VideoQuality{{FileID: "quality", Codec: "h265"}},
	}
	message := &RichMessage{Blocks: []RichBlock{{Type: RichBlockVideo, Video: video}}}
	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	video.FileID = "mutated"
	video.Thumbnail.FileID = "mutated"
	video.Cover[0].FileID = "mutated"
	video.Qualities[0].Codec = "mutated"
	projected := got.Media[0].Video
	if projected.FileID != "video" || projected.Thumbnail.FileID != "thumb" || projected.Cover[0].FileID != "cover" || projected.Qualities[0].Codec != "h265" {
		t.Fatalf("projected media aliases input: %#v", projected)
	}
}

func TestProjectEmptyAnchorLinkIntentionallyKeepsOnlyVisibleText(t *testing.T) {
	t.Parallel()

	message := &RichMessage{Blocks: []RichBlock{{Type: RichBlockParagraph, Text: &RichText{
		Kind: RichTextAnchorLink, Text: richPlain("back"), AnchorName: "",
	}}}}
	got, err := ProjectRichMessage(message)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if got.Markdown != "back" {
		t.Fatalf("empty top-anchor link emitted non-visible metadata: %q", got.Markdown)
	}
}
