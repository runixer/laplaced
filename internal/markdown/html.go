package markdown

import (
	"bytes"
	"errors"
	"html"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	extast "github.com/yuin/goldmark/v2/extension/ast"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/util"
	htmlparser "golang.org/x/net/html"
)

// TelegramHTMLRenderer is a custom renderer for Telegram HTML format
type TelegramHTMLRenderer struct {
	// safeLinks/preserveImageAlt are enabled only for the pre-rendered
	// Rich-Message fallback. The ordinary legacy renderer stays byte-compatible.
	safeLinks        bool
	preserveImageAlt bool
	// Table rendering state: plain-text cells of the rows seen so far.
	tableRows  [][]string
	currentRow []string
}

// NewTelegramHTMLRenderer creates a new TelegramHTMLRenderer
func NewTelegramHTMLRenderer() *TelegramHTMLRenderer {
	return &TelegramHTMLRenderer{}
}

func newSafeTelegramHTMLRenderer() *TelegramHTMLRenderer {
	return &TelegramHTMLRenderer{
		safeLinks:        true,
		preserveImageAlt: true,
	}
}

// nodeRenderers returns the renderer for every supported node kind. Kinds
// without an entry (the document, table bodies, link reference definitions)
// only render their children.
func (r *TelegramHTMLRenderer) nodeRenderers() nodeRenderers {
	return nodeRenderers{
		// Block nodes
		ast.KindHeading:       adapt(r.renderHeading),
		ast.KindBlockquote:    adapt(r.renderBlockquote),
		ast.KindCodeBlock:     adapt(r.renderCodeBlock),
		ast.KindHTMLBlock:     adapt(r.renderHTMLBlock),
		ast.KindList:          adapt(r.renderList),
		ast.KindListItem:      adapt(r.renderListItem),
		ast.KindParagraph:     adapt(r.renderParagraph),
		ast.KindThematicBreak: adapt(r.renderThematicBreak),

		// Inline nodes
		ast.KindAutoLink: adapt(r.renderAutoLink),
		ast.KindCodeSpan: adapt(r.renderCodeSpan),
		ast.KindEmphasis: adapt(r.renderEmphasis),
		ast.KindStrong:   adapt(r.renderStrong),
		ast.KindImage:    adapt(r.renderImage),
		ast.KindLink:     adapt(r.renderLink),
		ast.KindRawHTML:  adapt(r.renderRawHTML),
		ast.KindText:     adapt(r.renderText),
		KindSpoiler:      adapt(r.renderSpoiler),

		// GFM extension nodes
		extast.KindStrikethrough: adapt(r.renderStrikethrough),
		extast.KindTable:         adapt(r.renderTable),
		extast.KindTableHeader:   adapt(r.renderTableRow),
		extast.KindTableRow:      adapt(r.renderTableRow),
		extast.KindTableCell:     renderer.NodeRendererFunc(r.renderTableCell),
	}
}

// Block renderers

func (r *TelegramHTMLRenderer) renderHeading(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	// Telegram doesn't support headings, render as bold
	if entering {
		_, _ = w.WriteString("<b>")
	} else {
		_, _ = w.WriteString("</b>\n\n")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderBlockquote(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		// Add newline before blockquote if not at document start
		parent := n.Parent()
		if parent != nil && parent.Kind() != ast.KindDocument {
			_, _ = w.WriteString("\n")
		}
		_, _ = w.WriteString("<blockquote>")
	} else {
		_, _ = w.WriteString("</blockquote>\n\n")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderCodeBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		// Add newline before code block
		parent := n.Parent()
		if parent != nil && parent.Kind() != ast.KindDocument {
			_, _ = w.WriteString("\n")
		}

		// Check for language (fenced code blocks only)
		cb := n.(*ast.CodeBlock)
		if language, ok := cb.Language(source); ok {
			_, _ = w.WriteString("<pre><code class=\"language-")
			_, _ = w.WriteString(html.EscapeString(language))
			_, _ = w.WriteString("\">")
		} else {
			_, _ = w.WriteString("<pre><code>")
		}

		_, _ = w.Write(escapeHTML(cb.Value.Bytes(source)))
		_, _ = w.WriteString("</code></pre>\n\n")
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Skip raw HTML blocks for security
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderList(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Telegram doesn't support <ul> or <ol>, we'll emulate with bullets/numbers
	if entering {
		parent := node.Parent()
		// Add newline before list
		if parent != nil && parent.Kind() != ast.KindDocument {
			_, _ = w.WriteString("\n")
		}
	} else if node.NextSibling() != nil {
		// Add double newline after the entire list if there's a next sibling
		_, _ = w.WriteString("\n\n")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderListItem(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		// Determine if this is an ordered or unordered list
		parent := n.Parent()
		if parent != nil && parent.Kind() == ast.KindList {
			list := parent.(*ast.List)
			if list.IsOrdered() {
				// Get the item number
				itemNum := list.Start
				for sibling := parent.FirstChild(); sibling != nil && sibling != n; sibling = sibling.NextSibling() {
					itemNum++
				}
				_, _ = w.WriteString(strconv.Itoa(itemNum))
				_, _ = w.WriteString(". ")
			} else {
				_, _ = w.WriteString("• ")
			}
		}
	} else if n.NextSibling() != nil {
		// Add newline only if there's a next sibling (not the last item)
		_, _ = w.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderParagraph(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	// Don't render <p> tags, just add newlines between paragraphs
	if entering {
		// Telegram has no checkboxes; a task item opens with a ballot box.
		switch taskStatus(n) {
		case extension.TaskStatusCompleted:
			_, _ = w.WriteString("☑ ")
		case extension.TaskStatusActive:
			_, _ = w.WriteString("☐ ")
		}
	} else {
		// Add newline after paragraph, but not if it's inside a list item or blockquote
		parent := n.Parent()
		if parent != nil && (parent.Kind() == ast.KindListItem || parent.Kind() == ast.KindBlockquote) {
			// Don't add extra newline
			return ast.WalkContinue, nil
		}
		// Check if there's a next sibling (another paragraph)
		if n.NextSibling() != nil {
			_, _ = w.WriteString("\n\n")
		}
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderThematicBreak(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

// Inline renderers

func (r *TelegramHTMLRenderer) renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.AutoLink)
	if entering {
		// Email autolinks already carry the mailto: scheme in Destination.
		href := n.Destination.Value(source)
		label := []byte(n.Label.Value(source))
		if r.safeLinks && !safeRichURL(href) {
			_, _ = w.Write(escapeHTML(label))
			return ast.WalkSkipChildren, nil
		}
		_, _ = w.WriteString("<a href=\"")
		_, _ = w.Write(escapeHTML([]byte(href)))
		_, _ = w.WriteString("\">")
		_, _ = w.Write(escapeHTML(label))
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderCodeSpan(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<code>")
		_, _ = w.WriteString(html.EscapeString(n.(*ast.CodeSpan).Value.Value(source)))
		_, _ = w.WriteString("</code>")
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderEmphasis(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<i>")
	} else {
		_, _ = w.WriteString("</i>")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderStrong(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<b>")
	} else {
		_, _ = w.WriteString("</b>")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Telegram doesn't support inline images in text, skip
	if r.preserveImageAlt {
		// Rich fallback must preserve harmless visible alt text while never
		// inspecting or activating the model-authored media destination.
		return ast.WalkContinue, nil
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	destination := node.(*ast.Link).Destination.Value(source)
	if r.safeLinks && !safeRichURL(destination) {
		// Keep the visible label, discard the active wrapper/destination.
		return ast.WalkContinue, nil
	}
	if entering {
		_, _ = w.WriteString("<a href=\"")
		_, _ = w.WriteString(html.EscapeString(destination))
		_, _ = w.WriteString("\">")
	} else {
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Skip raw HTML for security
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderText(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.Text)
		_, _ = w.WriteString(html.EscapeString(n.Value.Value(source)))
		if n.HardLineBreak() {
			_, _ = w.WriteString("\n")
		} else if n.SoftLineBreak() {
			_, _ = w.WriteString("\n")
		}
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderSpoiler(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<tg-spoiler>")
	} else {
		_, _ = w.WriteString("</tg-spoiler>")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderStrikethrough(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<s>")
	} else {
		_, _ = w.WriteString("</s>")
	}
	return ast.WalkContinue, nil
}

// Table renderers
//
// Telegram has no tables. A table becomes a vertical record list in a <pre>
// block (see formatTableVertical), built from the plain text of each cell.

func (r *TelegramHTMLRenderer) renderTable(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.tableRows = nil
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(formatTableVertical(r.tableRows))
	_, _ = w.WriteString("\n")
	r.tableRows = nil
	return ast.WalkContinue, nil
}

// renderTableRow collects one row; the header row is the first row.
func (r *TelegramHTMLRenderer) renderTableRow(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.currentRow = []string{}
	} else {
		r.tableRows = append(r.tableRows, r.currentRow)
		r.currentRow = nil
	}
	return ast.WalkContinue, nil
}

// renderTableCell renders the cell's inline content with the same renderers
// and keeps only its visible text: link targets, formatting and spoilers do
// not survive inside a <pre> block.
func (r *TelegramHTMLRenderer) renderTableCell(_ util.BufWriter, source []byte, n ast.Node, entering bool, rc renderer.Context) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	var cell bytes.Buffer
	cellWriter := util.NewErrorBufWriter(&cell)
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if err := rc.Render(cellWriter, source, child); err != nil {
			return ast.WalkStop, err
		}
	}
	if err := cellWriter.Flush(); err != nil {
		return ast.WalkStop, err
	}
	r.currentRow = append(r.currentRow, htmlCellText(cell.String()))
	return ast.WalkSkipChildren, nil
}

// ToHTML converts Markdown to Telegram-compatible HTML
func ToHTML(markdown string) (string, error) {
	return toTelegramHTML(markdown, NewTelegramHTMLRenderer(), false)
}

// ToSafeLegacyHTML renders the policy-equivalent legacy representation used
// only as a preflighted fallback for Rich Messages. It preserves image alt text,
// removes unsafe/model-authored mention links and neutralizes bare @mentions so
// legacy entity auto-detection cannot bypass the rich skip_entity_detection
// boundary.
func ToSafeLegacyHTML(markdown string) (string, error) {
	return toTelegramHTML(markdown, newSafeTelegramHTMLRenderer(), true)
}

func toTelegramHTML(markdown string, nodeRenderer *TelegramHTMLRenderer, neutralizeMentions bool) (string, error) {
	// Convert LaTeX to Unicode BEFORE markdown parsing
	markdown = convertLatexToUnicode(markdown)

	source := []byte(markdown)
	root := newMarkdownParser(spoilerInlineParser(500)).Parse(source)
	result, err := renderNode(source, root, nodeRenderer.nodeRenderers())
	if err != nil {
		return "", err
	}

	// Trim trailing newlines
	result = strings.TrimRight(result, "\n")
	if neutralizeMentions {
		result = neutralizeTelegramMentionsInHTML(result)
	}

	return result, nil
}

// SafeLegacyPlainText escapes a last-resort plain-text chunk and applies the
// same mention neutralization as ToSafeLegacyHTML.
func SafeLegacyPlainText(text string) string {
	return html.EscapeString(neutralizeUnsafeTelegramEntities(text))
}

func neutralizeTelegramMentionsInHTML(input string) string {
	z := htmlparser.NewTokenizer(strings.NewReader(input))
	var out strings.Builder
	protectedDepth := 0
	for {
		typeOfToken := z.Next()
		switch typeOfToken {
		case htmlparser.ErrorToken:
			if err := z.Err(); err != nil && !errors.Is(err, io.EOF) {
				// Generated legacy HTML should always tokenize. Fail closed to
				// escaped visible text if that invariant is ever broken.
				return html.EscapeString(input)
			}
			return out.String()
		case htmlparser.TextToken:
			tok := z.Token()
			if protectedDepth == 0 {
				tok.Data = neutralizeUnsafeTelegramEntities(tok.Data)
			}
			out.WriteString(tok.String())
		case htmlparser.StartTagToken:
			tok := z.Token()
			if tok.Data == "a" || tok.Data == "code" || tok.Data == "pre" {
				protectedDepth++
			}
			out.WriteString(tok.String())
		case htmlparser.EndTagToken:
			tok := z.Token()
			out.WriteString(tok.String())
			if (tok.Data == "a" || tok.Data == "code" || tok.Data == "pre") && protectedDepth > 0 {
				protectedDepth--
			}
		default:
			out.WriteString(z.Token().String())
		}
	}
}

func neutralizeUnsafeTelegramEntities(text string) string {
	text = neutralizeBareTelegramMentions(text)
	var out strings.Builder
	for i := 0; i < len(text); {
		if i+5 <= len(text) && strings.EqualFold(text[i:i+5], "tg://") {
			out.WriteString(text[i : i+3])
			out.WriteRune('\u2060') // break Telegram deep-link auto-detection
			out.WriteString(text[i+3 : i+5])
			i += 5
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

func neutralizeBareTelegramMentions(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		out.WriteByte(text[i])
		if text[i] == '@' && i+1 < len(text) && isTelegramUsernameByte(text[i+1]) {
			out.WriteRune('\u2060') // invisible WORD JOINER breaks entity detection
		}
	}
	return out.String()
}

func isTelegramUsernameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// formatTableVertical formats table data in vertical format (one record per block)
func formatTableVertical(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}

	var buf bytes.Buffer
	buf.WriteString("<pre>")

	// First row is headers
	headers := rows[0]
	dataRows := rows[1:]

	// Find max header width for alignment
	maxHeaderWidth := 0
	for _, header := range headers {
		width := utf8.RuneCountInString(header)
		if width > maxHeaderWidth {
			maxHeaderWidth = width
		}
	}

	// Render each data row as a separate block
	for rowIdx, row := range dataRows {
		// Add separator before each record (except first)
		if rowIdx > 0 {
			buf.WriteString("\n")
			// Add visual separator line
			buf.WriteString(strings.Repeat("─", maxHeaderWidth+2))
			buf.WriteString("\n")
		}

		// Render each field
		for colIdx, cell := range row {
			if colIdx < len(headers) {
				header := headers[colIdx]
				headerWidth := utf8.RuneCountInString(header)
				padding := maxHeaderWidth - headerWidth

				// Format: "Header: value"
				// Escape HTML entities in both header and cell
				buf.WriteString(html.EscapeString(header))
				buf.WriteString(strings.Repeat(" ", padding))
				buf.WriteString(": ")
				buf.WriteString(html.EscapeString(cell))

				// Add newline (except for last field)
				if colIdx < len(headers)-1 {
					buf.WriteString("\n")
				}
			}
		}
	}

	buf.WriteString("</pre>")

	return buf.String()
}

// htmlCellText returns the visible text of a rendered table cell.
func htmlCellText(cellHTML string) string {
	doc, err := htmlparser.Parse(strings.NewReader(cellHTML))
	if err != nil {
		// Rendered cell HTML always parses; fall back to the raw markup.
		return strings.TrimSpace(cellHTML)
	}
	return extractText(doc)
}

// extractText recursively extracts text content from an HTML node
func extractText(n *htmlparser.Node) string {
	if n.Type == htmlparser.TextNode {
		return n.Data
	}

	var buf bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		buf.WriteString(extractText(c))
	}
	return strings.TrimSpace(buf.String())
}

// Helper functions

func escapeHTML(b []byte) []byte {
	return []byte(html.EscapeString(string(b)))
}

// UTF16Length calculates the length of a string in UTF-16 code units
// This is how Telegram counts message length
func UTF16Length(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// HardSplitUTF16 splits s into pieces of at most limit UTF-16 code units,
// cutting on rune boundaries and preferring a whitespace boundary near the
// cut. Last-resort plain-text splitter for content that cannot be delivered
// formatted within the budget.
func HardSplitUTF16(s string, limit int) []string {
	if limit <= 0 || UTF16Length(s) <= limit {
		return []string{s}
	}

	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		units := 0
		cut := 0
		for i, r := range runes {
			u := 1
			if r > 0xFFFF {
				u = 2
			}
			if units+u > limit {
				break
			}
			units += u
			cut = i + 1
		}
		if cut == 0 {
			cut = 1 // single astral rune over a tiny limit: keep making progress
		}
		// Prefer a whitespace boundary within the tail of the piece.
		if cut < len(runes) {
			for i := cut - 1; i > 0 && i > cut-100; i-- {
				if unicode.IsSpace(runes[i]) {
					cut = i
					break
				}
			}
		}
		if piece := strings.TrimSpace(string(runes[:cut])); piece != "" {
			out = append(out, piece)
		}
		runes = runes[cut:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	return out
}
