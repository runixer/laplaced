package markdown

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
	htmlparser "golang.org/x/net/html"
)

// TelegramHTMLRenderer is a custom renderer for Telegram HTML format
type TelegramHTMLRenderer struct {
	ghtml.Config
	// Table rendering state
	tableData   [][]string
	currentRow  []string
	currentCell *bytes.Buffer
	alignments  []extast.Alignment
}

// NewTelegramHTMLRenderer creates a new TelegramHTMLRenderer
func NewTelegramHTMLRenderer(opts ...ghtml.Option) renderer.NodeRenderer {
	r := &TelegramHTMLRenderer{
		Config: ghtml.NewConfig(),
	}
	for _, opt := range opts {
		opt.SetHTMLOption(&r.Config)
	}
	return r
}

// RegisterFuncs implements renderer.NodeRenderer.RegisterFuncs
func (r *TelegramHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	// Block nodes
	reg.Register(ast.KindDocument, r.renderDocument)
	reg.Register(ast.KindHeading, r.renderHeading)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindCodeBlock, r.renderCodeBlock)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)
	reg.Register(ast.KindList, r.renderList)
	reg.Register(ast.KindListItem, r.renderListItem)
	reg.Register(ast.KindParagraph, r.renderParagraph)
	reg.Register(ast.KindTextBlock, r.renderTextBlock)
	reg.Register(ast.KindThematicBreak, r.renderThematicBreak)

	// Inline nodes
	reg.Register(ast.KindAutoLink, r.renderAutoLink)
	reg.Register(ast.KindCodeSpan, r.renderCodeSpan)
	reg.Register(ast.KindEmphasis, r.renderEmphasis)
	reg.Register(ast.KindImage, r.renderImage)
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderString)

	// GFM extension nodes - strikethrough
	// Note: extension.Strikethrough is the actual node type
	strikethroughKind := ast.NewNodeKind("Strikethrough")
	reg.Register(strikethroughKind, r.renderStrikethrough)

	// GFM extension nodes - tables
	reg.Register(extast.KindTable, r.renderTable)
	reg.Register(extast.KindTableHeader, r.renderTableHeader)
	reg.Register(extast.KindTableRow, r.renderTableRow)
	reg.Register(extast.KindTableCell, r.renderTableCell)
}

// Block renderers

func (r *TelegramHTMLRenderer) renderDocument(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Document node doesn't need any output
	return ast.WalkContinue, nil
}

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
		_, _ = w.WriteString("<pre><code>")
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			_, _ = w.Write(escapeHTML(line.Value(source)))
		}
		_, _ = w.WriteString("</code></pre>\n\n")
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		// Add newline before code block
		parent := n.Parent()
		if parent != nil && parent.Kind() != ast.KindDocument {
			_, _ = w.WriteString("\n")
		}
		_, _ = w.WriteString("<pre>")

		// Check for language
		cb := n.(*ast.FencedCodeBlock)
		language := cb.Language(source)
		if language != nil {
			_, _ = w.WriteString("<code class=\"language-")
			_, _ = w.Write(language)
			_, _ = w.WriteString("\">")
		} else {
			_, _ = w.WriteString("<code>")
		}

		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			_, _ = w.Write(escapeHTML(line.Value(source)))
		}
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
	} else {
		// Add double newline after the entire list if there's a next sibling
		if node.NextSibling() != nil {
			_, _ = w.WriteString("\n\n")
		}
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
				itemNum := 1
				for sibling := parent.FirstChild(); sibling != nil && sibling != n; sibling = sibling.NextSibling() {
					itemNum++
				}
				_, _ = w.WriteString(string(rune('0' + itemNum)))
				_, _ = w.WriteString(". ")
			} else {
				_, _ = w.WriteString("• ")
			}
		}
	} else {
		// Add newline only if there's a next sibling (not the last item)
		if n.NextSibling() != nil {
			_, _ = w.WriteString("\n")
		}
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderParagraph(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	// Don't render <p> tags, just add newlines between paragraphs
	if !entering {
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

func (r *TelegramHTMLRenderer) renderTextBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		// Don't add newline if TextBlock is inside a ListItem
		parent := n.Parent()
		if parent != nil && parent.Kind() == ast.KindListItem {
			// ListItem will handle newlines
			return ast.WalkContinue, nil
		}
		_, _ = w.WriteString("\n")
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
		_, _ = w.WriteString("<a href=\"")
		url := n.URL(source)
		_, _ = w.Write(url)
		_, _ = w.WriteString("\">")
		_, _ = w.Write(url)
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderCodeSpan(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<code>")
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			segment := c.(*ast.Text).Segment
			value := segment.Value(source)
			_, _ = w.Write(escapeHTML(value))
		}
		_, _ = w.WriteString("</code>")
	}
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderEmphasis(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Emphasis)
	tag := "i"
	if n.Level == 2 {
		tag = "b"
	}
	if entering {
		_, _ = w.WriteString("<")
		_, _ = w.WriteString(tag)
		_, _ = w.WriteString(">")
	} else {
		_, _ = w.WriteString("</")
		_, _ = w.WriteString(tag)
		_, _ = w.WriteString(">")
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Telegram doesn't support inline images in text, skip
	return ast.WalkSkipChildren, nil
}

func (r *TelegramHTMLRenderer) renderLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if entering {
		_, _ = w.WriteString("<a href=\"")
		_, _ = w.Write(n.Destination)
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
		segment := n.Segment
		_, _ = w.Write(escapeHTML(segment.Value(source)))
		if n.HardLineBreak() {
			_, _ = w.WriteString("\n")
		} else if n.SoftLineBreak() {
			_, _ = w.WriteString("\n")
		}
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderString(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.String)
		_, _ = w.Write(escapeHTML(n.Value))
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

func (r *TelegramHTMLRenderer) renderTable(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		// Initialize table state
		r.tableData = [][]string{}

		// Get alignments from table node
		table := n.(*extast.Table)
		r.alignments = table.Alignments
	} else {
		// Render the complete table as monospace text
		tableText := r.formatTableAsMonospace()

		// Add newline before table if not at document start
		parent := n.Parent()
		if parent != nil && parent.Kind() != ast.KindDocument {
			_, _ = w.WriteString("\n")
		}

		_, _ = w.WriteString("<pre>")
		_, _ = w.WriteString(tableText)
		_, _ = w.WriteString("</pre>\n\n")

		// Clear table state
		r.tableData = nil
		r.alignments = nil
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderTableHeader(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	// TableHeader is just a container, no special rendering needed
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderTableRow(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.currentRow = []string{}
	} else {
		// Add completed row to table data
		r.tableData = append(r.tableData, r.currentRow)
		r.currentRow = nil
	}
	return ast.WalkContinue, nil
}

func (r *TelegramHTMLRenderer) renderTableCell(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		// Start capturing cell content
		r.currentCell = &bytes.Buffer{}
	} else {
		// Extract text content from cell
		cellContent := r.extractCellText(n, source)
		r.currentRow = append(r.currentRow, cellContent)
		r.currentCell = nil
	}
	return ast.WalkContinue, nil
}

// extractCellText recursively extracts plain text from a table cell
func (r *TelegramHTMLRenderer) extractCellText(n ast.Node, source []byte) string {
	var buf bytes.Buffer

	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.Kind() {
		case ast.KindText:
			text := child.(*ast.Text)
			buf.Write(text.Segment.Value(source))
		case ast.KindString:
			str := child.(*ast.String)
			buf.Write(str.Value)
		case ast.KindCodeSpan:
			// Extract code span content
			for c := child.FirstChild(); c != nil; c = c.NextSibling() {
				if c.Kind() == ast.KindText {
					text := c.(*ast.Text)
					buf.Write(text.Segment.Value(source))
				}
			}
		case ast.KindEmphasis:
			// Recursively extract from emphasis
			buf.WriteString(r.extractCellText(child, source))
		case ast.KindLink:
			// Extract link text
			buf.WriteString(r.extractCellText(child, source))
		default:
			// Try to extract text from other node types
			buf.WriteString(r.extractCellText(child, source))
		}
	}

	return strings.TrimSpace(buf.String())
}

// formatTableAsMonospace formats the table data as aligned monospace text
func (r *TelegramHTMLRenderer) formatTableAsMonospace() string {
	if len(r.tableData) == 0 {
		return ""
	}

	// Calculate column widths
	numCols := len(r.tableData[0])
	colWidths := make([]int, numCols)

	for _, row := range r.tableData {
		for i, cell := range row {
			if i < numCols {
				width := utf8.RuneCountInString(cell)
				if width > colWidths[i] {
					colWidths[i] = width
				}
			}
		}
	}

	var buf bytes.Buffer

	// Render each row
	for rowIdx, row := range r.tableData {
		for colIdx, cell := range row {
			if colIdx < numCols {
				// Determine alignment
				align := extast.AlignNone
				if colIdx < len(r.alignments) {
					align = r.alignments[colIdx]
				}

				// Pad cell according to alignment
				cellWidth := utf8.RuneCountInString(cell)
				padding := colWidths[colIdx] - cellWidth

				switch align {
				case extast.AlignLeft, extast.AlignNone:
					buf.WriteString(cell)
					buf.WriteString(strings.Repeat(" ", padding))
				case extast.AlignRight:
					buf.WriteString(strings.Repeat(" ", padding))
					buf.WriteString(cell)
				case extast.AlignCenter:
					leftPad := padding / 2
					rightPad := padding - leftPad
					buf.WriteString(strings.Repeat(" ", leftPad))
					buf.WriteString(cell)
					buf.WriteString(strings.Repeat(" ", rightPad))
				}

				// Add column separator (except for last column)
				if colIdx < numCols-1 {
					buf.WriteString(" │ ")
				}
			}
		}

		// Add separator line after header (first row)
		if rowIdx == 0 && len(r.tableData) > 1 {
			buf.WriteString("\n")
			for colIdx := 0; colIdx < numCols; colIdx++ {
				buf.WriteString(strings.Repeat("─", colWidths[colIdx]))
				if colIdx < numCols-1 {
					buf.WriteString("─┼─")
				}
			}
		}

		// Add newline (except for last row)
		if rowIdx < len(r.tableData)-1 {
			buf.WriteString("\n")
		}
	}

	return buf.String()
}

// ToHTML converts Markdown to Telegram-compatible HTML
func ToHTML(markdown string) (string, error) {
	// Convert LaTeX to Unicode BEFORE markdown parsing
	markdown = convertLatexToUnicode(markdown)

	// Create Goldmark with GFM and custom renderer
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			NewSpoilerExtension(),
		),
		goldmark.WithRenderer(
			renderer.NewRenderer(
				renderer.WithNodeRenderers(
					util.Prioritized(NewTelegramHTMLRenderer(), 1000),
				),
			),
		),
	)

	var buf bytes.Buffer
	if err := md.Convert([]byte(markdown), &buf); err != nil {
		return "", err
	}

	result := buf.String()

	// Convert HTML tables to monospace text BEFORE other replacements
	result = convertTablesToMonospace(result)

	// Remove <p> tags (Telegram doesn't support them)
	result = strings.ReplaceAll(result, "<p>", "")
	result = strings.ReplaceAll(result, "</p>", "")

	// Trim trailing newlines
	result = strings.TrimRight(result, "\n")

	// Replace HTML tags with Telegram-compatible equivalents
	result = strings.ReplaceAll(result, "<del>", "<s>")
	result = strings.ReplaceAll(result, "</del>", "</s>")
	result = strings.ReplaceAll(result, "<strong>", "<b>")
	result = strings.ReplaceAll(result, "</strong>", "</b>")
	result = strings.ReplaceAll(result, "<em>", "<i>")
	result = strings.ReplaceAll(result, "</em>", "</i>")

	// Remove unsupported <input> tags from GFM task lists and replace with emoji
	// Checked: <input checked="" disabled="" type="checkbox"> → ☑
	// Unchecked: <input disabled="" type="checkbox"> → ☐
	result = strings.ReplaceAll(result, `<input checked="" disabled="" type="checkbox">`, "☑")
	result = strings.ReplaceAll(result, `<input disabled="" type="checkbox">`, "☐")
	// Also handle variations in attribute order
	result = strings.ReplaceAll(result, `<input type="checkbox" checked="" disabled="">`, "☑")
	result = strings.ReplaceAll(result, `<input type="checkbox" disabled="">`, "☐")
	result = strings.ReplaceAll(result, `<input type="checkbox" disabled="" checked="">`, "☑")

	return result, nil
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

// convertTablesToMonospace converts HTML tables to monospace text blocks
func convertTablesToMonospace(htmlStr string) string {
	// Find all table tags
	tableRegex := regexp.MustCompile(`(?s)<table>.*?</table>`)

	return tableRegex.ReplaceAllStringFunc(htmlStr, func(tableHTML string) string {
		// Parse the table HTML
		doc, err := htmlparser.Parse(strings.NewReader(tableHTML))
		if err != nil {
			// If parsing fails, return original
			return tableHTML
		}

		// Extract table data
		var rows [][]string
		var alignments []string

		var extractTable func(*htmlparser.Node)
		extractTable = func(n *htmlparser.Node) {
			if n.Type == htmlparser.ElementNode {
				switch n.Data {
				case "tr":
					var row []string
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == htmlparser.ElementNode && (c.Data == "th" || c.Data == "td") {
							cellText := extractText(c)
							row = append(row, cellText)

							// Extract alignment from style attribute (only for first row)
							if len(rows) == 0 && c.Data == "th" {
								align := "left"
								for _, attr := range c.Attr {
									if attr.Key == "style" && strings.Contains(attr.Val, "text-align") {
										if strings.Contains(attr.Val, "center") {
											align = "center"
										} else if strings.Contains(attr.Val, "right") {
											align = "right"
										}
									}
								}
								alignments = append(alignments, align)
							}
						}
					}
					if len(row) > 0 {
						rows = append(rows, row)
					}
				}
			}

			for c := n.FirstChild; c != nil; c = c.NextSibling {
				extractTable(c)
			}
		}

		extractTable(doc)

		if len(rows) == 0 {
			return tableHTML
		}

		// Always use vertical format for reliability and better mobile readability
		// Horizontal format has alignment issues with HTML entities and proportional fonts
		return formatTableVertical(rows)
	})
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
