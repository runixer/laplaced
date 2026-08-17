package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	stdhtml "html"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// RichStats describes the Telegram rich-message limits represented by a
// rendered payload. Characters counts visible Unicode code points (including
// formula source); Blocks counts emitted structural blocks and nested list
// items/table rows; MaxDepth counts nested emitted tags; MaxTableColumns is the
// widest GFM table. The renderer reports these values but deliberately does not
// enforce Telegram's transport limits or split messages.
type RichStats struct {
	Characters      int
	Blocks          int
	MaxDepth        int
	MaxTableColumns int
}

// RichFragmentKind identifies the top-level structural unit represented by a
// RichFragment. A fragment kind describes the rendered structure rather than
// the Markdown spelling (for example, both fenced and indented code are code
// fragments).
type RichFragmentKind string

const (
	RichFragmentParagraph  RichFragmentKind = "paragraph"
	RichFragmentHeading    RichFragmentKind = "heading"
	RichFragmentList       RichFragmentKind = "list"
	RichFragmentTable      RichFragmentKind = "table"
	RichFragmentCode       RichFragmentKind = "code"
	RichFragmentMath       RichFragmentKind = "math"
	RichFragmentBlockquote RichFragmentKind = "blockquote"
	RichFragmentDivider    RichFragmentKind = "divider"
	RichFragmentRaw        RichFragmentKind = "raw"
	RichFragmentOther      RichFragmentKind = "other"
)

// RichSourceRange is a half-open byte range in the exact canonical input
// passed to ParseRichFragments.
type RichSourceRange struct {
	Start int
	End   int
}

// RichFragment is one complete top-level Markdown block rendered through the
// Rich HTML allowlist. Atomic is deliberately explicit: a delivery packer may
// group adjacent fragments, but must not split an atomic fragment's HTML or
// LegacySource. SourceStart and SourceEnd are byte offsets into the exact input
// passed to ParseRichFragments, and LegacySource is that byte-identical slice.
// Blank lines between blocks belong to the preceding fragment so the ordered
// LegacySource values concatenate back to the input without normalization.
type RichFragment struct {
	Kind         RichFragmentKind
	Atomic       bool
	SourceStart  int
	SourceEnd    int
	LegacySource string
	HTML         string
	Stats        RichStats
	// BoundaryTextRanges are exact source ranges of direct Text children of a
	// top-level paragraph. Application protocol markers may only be recognized
	// inside these ranges; syntax owned by any inline container is excluded.
	BoundaryTextRanges []RichSourceRange
}

// RichDocument is the immutable result of one canonical Markdown parse. HTML
// and Stats are exact aggregates of Fragments in order. LegacySource is kept
// byte-identical even when the rich parser applies its narrow list-boundary
// normalization internally.
type RichDocument struct {
	LegacySource string
	HTML         string
	Stats        RichStats
	Fragments    []RichFragment
}

// richMathNode protects formula source from Markdown parsing. In particular,
// LaTeX underscores, asterisks and angle brackets must stay formula text rather
// than becoming emphasis or raw HTML nodes.
type richMathNode struct {
	ast.BaseInline
	content string
	display bool
}

var kindRichMath = ast.NewNodeKind("RichMath")

func (n *richMathNode) Kind() ast.NodeKind { return kindRichMath }
func (n *richMathNode) Inline()            {}

func (n *richMathNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Display": strconv.FormatBool(n.display),
	}, nil)
}

// richMathParser reuses the established LaTeX delimiter/currency matchers from
// latex.go. It runs before ordinary inline parsing, so code spans are still
// consumed by Goldmark's backtick parser and link destinations are never
// inspected as message text.
type richMathParser struct{}

func (p *richMathParser) Trigger() []byte { return []byte{'$', '\\'} }

func (p *richMathParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) == 0 {
		return nil
	}

	// Inline math cannot cross a line. Only collect the more expensive logical
	// paragraph remainder for a display opener; this also avoids quadratic work
	// on ordinary backslash-heavy prose.
	displayOpener := bytes.HasPrefix(line, []byte("$$")) ||
		bytes.HasPrefix(line, []byte("\\[")) ||
		bytes.HasPrefix(line, []byte("$\n"))
	if displayOpener {
		runes := []rune(richReaderRemainder(block))
		content, raw, end := matchDisplayMath(runes, 0)
		if end <= 0 {
			return nil
		}
		content = strings.TrimSpace(content)
		if content == "" {
			return nil
		}
		block.Advance(len(raw))
		return &richMathNode{content: content, display: true}
	}
	if line[0] != '$' {
		return nil
	}

	runes := []rune(string(line))
	if content, raw, end := matchInlineMath(runes, 0); end > 0 {
		if strings.TrimSpace(content) == "" {
			return nil
		}
		block.Advance(len(raw))
		return &richMathNode{content: content}
	}
	return nil
}

// richReaderRemainder returns the logical remainder of a paragraph without
// leaking blockquote/list indentation into a multi-line formula. InlineParser
// is allowed to parse beyond the current line; save and restore the reader so a
// failed match is side-effect free.
func richReaderRemainder(reader text.Reader) string {
	line, pos := reader.Position()
	defer reader.SetPosition(line, pos)

	var out strings.Builder
	for {
		part, _ := reader.PeekLine()
		if len(part) == 0 {
			break
		}
		out.Write(part)
		reader.Advance(len(part))
	}
	return out.String()
}

type richHTMLRenderer struct {
	writer        ghtml.Writer
	stats         RichStats
	depth         int
	suppressLinks bool
}

func newRichHTMLRenderer() *richHTMLRenderer {
	return &richHTMLRenderer{writer: ghtml.NewWriter()}
}

func newRichHTMLPreviewRenderer() *richHTMLRenderer {
	return &richHTMLRenderer{
		writer:        ghtml.NewWriter(),
		suppressLinks: true,
	}
}

func (r *richHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
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

	reg.Register(ast.KindAutoLink, r.renderAutoLink)
	reg.Register(ast.KindCodeSpan, r.renderCodeSpan)
	reg.Register(ast.KindEmphasis, r.renderEmphasis)
	reg.Register(ast.KindImage, r.renderImage)
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderString)

	reg.Register(extast.KindStrikethrough, r.renderStrikethrough)
	reg.Register(extast.KindTaskCheckBox, r.renderTaskCheckBox)
	reg.Register(extast.KindTable, r.renderTable)
	reg.Register(extast.KindTableHeader, r.renderTableHeader)
	reg.Register(extast.KindTableRow, r.renderTableRow)
	reg.Register(extast.KindTableCell, r.renderTableCell)
	reg.Register(KindSpoiler, r.renderSpoiler)
	reg.Register(kindRichMath, r.renderMath)
}

func (r *richHTMLRenderer) open(w util.BufWriter, tag string) {
	_, _ = w.WriteString("<")
	_, _ = w.WriteString(tag)
	_, _ = w.WriteString(">")
	r.depth++
	if r.depth > r.stats.MaxDepth {
		r.stats.MaxDepth = r.depth
	}
}

func (r *richHTMLRenderer) openAttrs(w util.BufWriter, tag, attrs string) {
	_, _ = w.WriteString("<")
	_, _ = w.WriteString(tag)
	_, _ = w.WriteString(attrs)
	_, _ = w.WriteString(">")
	r.depth++
	if r.depth > r.stats.MaxDepth {
		r.stats.MaxDepth = r.depth
	}
}

func (r *richHTMLRenderer) close(w util.BufWriter, tag string) {
	_, _ = w.WriteString("</")
	_, _ = w.WriteString(tag)
	_, _ = w.WriteString(">")
	if r.depth > 0 {
		r.depth--
	}
}

func (r *richHTMLRenderer) leaf(w util.BufWriter, value string) {
	_, _ = w.WriteString(value)
	if r.depth+1 > r.stats.MaxDepth {
		r.stats.MaxDepth = r.depth + 1
	}
}

func (r *richHTMLRenderer) block() { r.stats.Blocks++ }

func (r *richHTMLRenderer) writeText(w util.BufWriter, value []byte, raw bool) {
	var escaped bytes.Buffer
	escapedWriter := bufio.NewWriter(&escaped)
	if raw {
		r.writer.RawWrite(escapedWriter, value)
	} else {
		r.writer.Write(escapedWriter, value)
	}
	_ = escapedWriter.Flush()
	rendered := escaped.String()
	_, _ = w.WriteString(rendered)
	r.stats.Characters += utf8.RuneCountInString(stdhtml.UnescapeString(rendered))
}

func (r *richHTMLRenderer) writeCode(w util.BufWriter, value []byte) {
	rendered := stdhtml.EscapeString(string(value))
	_, _ = w.WriteString(rendered)
	r.stats.Characters += utf8.RuneCount(value)
}

func (r *richHTMLRenderer) renderDocument(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderHeading(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	tag := "h" + strconv.Itoa(node.(*ast.Heading).Level)
	if entering {
		r.block()
		r.open(w, tag)
	} else {
		r.close(w, tag)
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderBlockquote(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.block()
		r.open(w, "blockquote")
	} else {
		r.close(w, "blockquote")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	r.block()
	r.open(w, "pre")
	r.open(w, "code")
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		r.writeCode(w, line.Value(source))
	}
	r.close(w, "code")
	r.close(w, "pre")
	return ast.WalkSkipChildren, nil
}

func (r *richHTMLRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	r.block()
	r.open(w, "pre")
	language := safeCodeLanguage(node.(*ast.FencedCodeBlock).Language(source))
	if language == "" {
		r.open(w, "code")
	} else {
		r.openAttrs(w, "code", ` class="language-`+stdhtml.EscapeString(language)+`"`)
	}
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		r.writeCode(w, line.Value(source))
	}
	r.close(w, "code")
	r.close(w, "pre")
	return ast.WalkSkipChildren, nil
}

func safeCodeLanguage(language []byte) string {
	if len(language) == 0 || len(language) > 32 {
		return ""
	}
	for _, c := range string(language) {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '+' && c != '-' {
			return ""
		}
	}
	return string(language)
}

func (r *richHTMLRenderer) renderHTMLBlock(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
	return ast.WalkSkipChildren, nil
}

func (r *richHTMLRenderer) renderList(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	list := node.(*ast.List)
	tag := "ul"
	attrs := ""
	if list.IsOrdered() {
		tag = "ol"
		if list.Start != 1 {
			attrs = ` start="` + strconv.Itoa(list.Start) + `"`
		}
	}
	if entering {
		r.block()
		r.openAttrs(w, tag, attrs)
	} else {
		r.close(w, tag)
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderListItem(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.block()
		r.open(w, "li")
	} else {
		r.close(w, "li")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderParagraph(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// A direct display formula is a block. Avoid invalid <p><tg-math-block>
	// nesting; mixed text becomes implicit rich text around the formula block.
	if paragraphHasDisplayMath(node) {
		return ast.WalkContinue, nil
	}
	if entering {
		r.block()
		r.open(w, "p")
	} else {
		r.close(w, "p")
	}
	return ast.WalkContinue, nil
}

func paragraphHasDisplayMath(node ast.Node) bool {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if mathNode, ok := child.(*richMathNode); ok && mathNode.display {
			return true
		}
	}
	return false
}

func (r *richHTMLRenderer) renderTextBlock(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderThematicBreak(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.block()
		r.leaf(w, "<hr/>")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	href := string(n.URL(source))
	if n.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(href), "mailto:") {
		href = "mailto:" + href
	}
	label := n.Label(source)
	if !r.suppressLinks && safeRichURL(href) {
		r.openAttrs(w, "a", ` href="`+stdhtml.EscapeString(href)+`"`)
		r.writeText(w, label, true)
		r.close(w, "a")
	} else {
		r.writeText(w, label, true)
	}
	return ast.WalkSkipChildren, nil
}

func (r *richHTMLRenderer) renderCodeSpan(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	r.open(w, "code")
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		segment := child.(*ast.Text).Segment
		value := segment.Value(source)
		if bytes.HasSuffix(value, []byte("\n")) {
			r.writeCode(w, value[:len(value)-1])
			r.writeCode(w, []byte(" "))
		} else {
			r.writeCode(w, value)
		}
	}
	r.close(w, "code")
	return ast.WalkSkipChildren, nil
}

func (r *richHTMLRenderer) renderEmphasis(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	tag := "em"
	if node.(*ast.Emphasis).Level == 2 {
		tag = "strong"
	}
	if entering {
		r.open(w, tag)
	} else {
		r.close(w, tag)
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderImage(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
	// Intentionally emit no <img> and never inspect the destination. Walking the
	// children preserves escaped alt text as harmless visible text.
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	href := string(node.(*ast.Link).Destination)
	if r.suppressLinks || !safeRichURL(href) {
		// Preserve the label, discard the unsafe destination and wrapper.
		return ast.WalkContinue, nil
	}
	if entering {
		r.openAttrs(w, "a", ` href="`+stdhtml.EscapeString(href)+`"`)
	} else {
		r.close(w, "a")
	}
	return ast.WalkContinue, nil
}

func safeRichURL(raw string) bool {
	// Link destinations are model-authored input. Keep the bound well below any
	// practical Telegram/client URL limit so a tiny visible label cannot smuggle
	// an arbitrarily large attribute through the semantic-character budget.
	const maxURLBytes = 4096
	if raw == "" || len(raw) > maxURLBytes || strings.TrimSpace(raw) != raw || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return false
	}
	if strings.HasPrefix(raw, "#") {
		anchor := raw[1:]
		if anchor == "" || len(anchor) > 128 {
			return false
		}
		for _, c := range anchor {
			if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '-' && c != '.' && c != ':' {
				return false
			}
		}
		return true
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != "" && strings.Contains(u.Opaque, "@")
	case "tel":
		if u.Opaque == "" {
			return false
		}
		for _, c := range u.Opaque {
			if !unicode.IsDigit(c) && !strings.ContainsRune("+-.() ", c) {
				return false
			}
		}
		return true
	default:
		// tg://user is deliberately excluded here. A direct mention has social
		// side effects and may only be built from trusted typed application data,
		// never from model-authored Markdown.
		return false
	}
}

func (r *richHTMLRenderer) renderRawHTML(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
	return ast.WalkSkipChildren, nil
}

func (r *richHTMLRenderer) renderText(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Text)
	r.writeText(w, n.Segment.Value(source), n.IsRaw())
	if n.HardLineBreak() {
		r.leaf(w, "<br>")
		r.stats.Characters++
	} else if n.SoftLineBreak() {
		_, _ = w.WriteString("\n")
		r.stats.Characters++
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderString(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.String)
		r.writeText(w, n.Value, n.IsRaw())
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderStrikethrough(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.open(w, "s")
	} else {
		r.close(w, "s")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderTaskCheckBox(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if node.(*extast.TaskCheckBox).IsChecked {
		r.leaf(w, `<input type="checkbox" checked>`)
	} else {
		r.leaf(w, `<input type="checkbox">`)
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderTable(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.block()
		r.open(w, "table")
		columns := len(node.(*extast.Table).Alignments)
		if columns > r.stats.MaxTableColumns {
			r.stats.MaxTableColumns = columns
		}
	} else {
		r.close(w, "table")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderTableHeader(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.block()
		r.open(w, "tr")
	} else {
		r.close(w, "tr")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderTableRow(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.block()
		r.open(w, "tr")
	} else {
		r.close(w, "tr")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderTableCell(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	cell := node.(*extast.TableCell)
	tag := "td"
	if node.Parent().Kind() == extast.KindTableHeader {
		tag = "th"
	}
	attrs := ""
	if cell.Alignment != extast.AlignNone {
		attrs = ` align="` + cell.Alignment.String() + `"`
	}
	if entering {
		r.openAttrs(w, tag, attrs)
	} else {
		r.close(w, tag)
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderSpoiler(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.open(w, "tg-spoiler")
	} else {
		r.close(w, "tg-spoiler")
	}
	return ast.WalkContinue, nil
}

func (r *richHTMLRenderer) renderMath(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*richMathNode)
	content := strings.ReplaceAll(n.content, "\x00", "\uFFFD")
	tag := "tg-math"
	if n.display && richMathCanBeBlock(node) {
		tag = "tg-math-block"
		r.block()
	}
	r.open(w, tag)
	_, _ = w.WriteString(stdhtml.EscapeString(content))
	r.stats.Characters += utf8.RuneCountInString(content)
	r.close(w, tag)
	return ast.WalkSkipChildren, nil
}

func richMathCanBeBlock(node ast.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case ast.KindHeading, ast.KindEmphasis, ast.KindLink, ast.KindImage,
			extast.KindStrikethrough, extast.KindTableCell, KindSpoiler:
			return false
		}
	}
	return true
}

// ToRichHTML renders canonical Markdown as a safe Telegram Rich HTML payload.
// It emits only an allowlisted textual/structural tag set. Raw HTML is ignored,
// Markdown images retain only their alt text, and unsafe links retain only their
// visible label, so model-controlled input cannot create rich media.
func ToRichHTML(input string) (string, RichStats, error) {
	return renderRichHTML(input, newRichHTMLRenderer())
}

// ToRichHTMLPreview renders a safe Telegram Rich HTML payload for a streaming
// draft. It shares ToRichHTML's parser and allowlist, but emits no active link
// or anchor elements: link labels remain visible as inert text. Telegram's
// skip_entity_detection transport option must also be enabled so visible bare
// URLs are not made clickable by a client during the preview.
func ToRichHTMLPreview(input string) (string, RichStats, error) {
	return renderRichHTML(input, newRichHTMLPreviewRenderer())
}

func renderRichHTML(input string, richRenderer *richHTMLRenderer) (string, RichStats, error) {
	if !utf8.ValidString(input) {
		return "", RichStats{}, fmt.Errorf("rich markdown is not valid UTF-8")
	}
	input = normalizeRichListBoundaries(input)

	md := newRichMarkdown(richRenderer)

	var out bytes.Buffer
	if err := md.Convert([]byte(input), &out); err != nil {
		return "", RichStats{}, fmt.Errorf("render rich markdown: %w", err)
	}
	return out.String(), richRenderer.stats, nil
}

func newRichMarkdown(richRenderer *richHTMLRenderer) goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithInlineParsers(
			util.Prioritized(&richMathParser{}, 50),
			util.Prioritized(defaultSpoilerParser, 450),
		)),
		goldmark.WithRenderer(renderer.NewRenderer(
			renderer.WithNodeRenderers(
				// Goldmark registers node renderers from high to low priority;
				// the lowest priority wins duplicate node kinds. Override GFM's
				// table/task/strike HTML renderers with our strict allowlist.
				util.Prioritized(richRenderer, 0),
			),
		)),
	)
}

// ParseRichFragments parses canonical Markdown once and renders every
// top-level block as an independently packable Rich HTML fragment. Every
// returned fragment is atomic: callers may combine adjacent fragments into a
// Telegram message, but must not split a fragment internally. This keeps
// lists, tables, quotes, code and display math structurally valid.
func ParseRichFragments(input string) (RichDocument, error) {
	document := RichDocument{LegacySource: input}
	if !utf8.ValidString(input) {
		return RichDocument{}, fmt.Errorf("rich markdown is not valid UTF-8")
	}

	normalized := normalizeRichListBoundaries(input)
	normalizedSource := []byte(normalized)
	markdownParser := newRichMarkdown(newRichHTMLRenderer()).Parser()
	root := markdownParser.Parse(text.NewReader(normalizedSource))
	if root.FirstChild() == nil {
		return document, nil
	}

	offsetMap, err := richNormalizedOffsetMap(input, normalized)
	if err != nil {
		return RichDocument{}, err
	}

	children := make([]ast.Node, 0, root.ChildCount())
	starts := make([]int, 0, root.ChildCount())
	for child := root.FirstChild(); child != nil; child = child.NextSibling() {
		children = append(children, child)
		if len(children) == 1 {
			// Preserve leading blank lines as part of the first legacy source.
			starts = append(starts, 0)
			continue
		}

		normalizedStart, startErr := richTopLevelLineStart(child, normalizedSource)
		if startErr != nil {
			return RichDocument{}, fmt.Errorf("locate rich fragment %d: %w", len(children)-1, startErr)
		}
		originalStart := offsetMap[normalizedStart]
		if originalStart <= starts[len(starts)-1] || originalStart > len(input) {
			return RichDocument{}, fmt.Errorf(
				"locate rich fragment %d: invalid source boundary %d after %d",
				len(children)-1, originalStart, starts[len(starts)-1],
			)
		}
		starts = append(starts, originalStart)
	}

	document.Fragments = make([]RichFragment, 0, len(children))
	var htmlBuilder strings.Builder
	for i, child := range children {
		end := len(input)
		if i+1 < len(starts) {
			end = starts[i+1]
		}

		html, stats, renderErr := renderRichFragmentNode(normalizedSource, child)
		if renderErr != nil {
			return RichDocument{}, fmt.Errorf("render rich fragment %d: %w", i, renderErr)
		}
		boundaryRanges, rangeErr := richBoundaryTextRanges(child, offsetMap)
		if rangeErr != nil {
			return RichDocument{}, fmt.Errorf("locate boundary text ranges in rich fragment %d: %w", i, rangeErr)
		}
		fragment := RichFragment{
			Kind:               richFragmentKind(child),
			Atomic:             true,
			SourceStart:        starts[i],
			SourceEnd:          end,
			LegacySource:       input[starts[i]:end],
			HTML:               html,
			Stats:              stats,
			BoundaryTextRanges: boundaryRanges,
		}
		document.Fragments = append(document.Fragments, fragment)
		htmlBuilder.WriteString(html)
		mergeRichStats(&document.Stats, stats)
	}
	document.HTML = htmlBuilder.String()
	return document, nil
}

func renderRichFragmentNode(source []byte, node ast.Node) (string, RichStats, error) {
	richRenderer := newRichHTMLRenderer()
	nodeRenderer := renderer.NewRenderer(renderer.WithNodeRenderers(
		util.Prioritized(richRenderer, 0),
	))
	var out bytes.Buffer
	if err := nodeRenderer.Render(&out, source, node); err != nil {
		return "", RichStats{}, err
	}
	return out.String(), richRenderer.stats, nil
}

func richFragmentKind(node ast.Node) RichFragmentKind {
	switch node.Kind() {
	case ast.KindHeading:
		return RichFragmentHeading
	case ast.KindList:
		return RichFragmentList
	case extast.KindTable:
		return RichFragmentTable
	case ast.KindCodeBlock, ast.KindFencedCodeBlock:
		return RichFragmentCode
	case ast.KindBlockquote:
		return RichFragmentBlockquote
	case ast.KindThematicBreak:
		return RichFragmentDivider
	case ast.KindHTMLBlock:
		return RichFragmentRaw
	case ast.KindParagraph:
		if paragraphHasDisplayMath(node) {
			return RichFragmentMath
		}
		return RichFragmentParagraph
	default:
		return RichFragmentOther
	}
}

func mergeRichStats(total *RichStats, next RichStats) {
	total.Characters += next.Characters
	total.Blocks += next.Blocks
	if next.MaxDepth > total.MaxDepth {
		total.MaxDepth = next.MaxDepth
	}
	if next.MaxTableColumns > total.MaxTableColumns {
		total.MaxTableColumns = next.MaxTableColumns
	}
}

// richBoundaryTextRanges is a positive allowlist for application protocol
// boundaries. Only source owned by a Text node directly under a top-level
// paragraph is eligible. Text under code, emphasis, links, images, spoilers or
// any future inline container is deliberately excluded; hidden syntax such as
// link titles/references and raw HTML has no Text node and is excluded too.
func richBoundaryTextRanges(node ast.Node, offsetMap []int) ([]RichSourceRange, error) {
	if node.Kind() != ast.KindParagraph {
		return nil, nil
	}

	var ranges []RichSourceRange
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		textNode, ok := child.(*ast.Text)
		if !ok {
			continue
		}
		start, end := textNode.Segment.Start, textNode.Segment.Stop
		if start < 0 || end < start || start >= len(offsetMap) || end >= len(offsetMap) {
			return nil, fmt.Errorf("boundary text range [%d,%d) exceeds normalized source", start, end)
		}
		originalStart, originalEnd := offsetMap[start], offsetMap[end]
		if originalStart < originalEnd {
			ranges = append(ranges, RichSourceRange{Start: originalStart, End: originalEnd})
		}
	}
	return ranges, nil
}

// richTopLevelLineStart expands a block's AST position back to the beginning
// of its physical source line so Markdown container markers remain in the
// byte-identical LegacySource slice.
func richTopLevelLineStart(node ast.Node, source []byte) (int, error) {
	position := len(source)
	if err := ast.Walk(node, func(current ast.Node, _ bool) (ast.WalkStatus, error) {
		if pos := current.Pos(); pos >= 0 && pos < position {
			position = pos
		}
		if current.Type() == ast.TypeBlock {
			lines := current.Lines()
			for i := 0; i < lines.Len(); i++ {
				if start := lines.At(i).Start; start >= 0 && start < position {
					position = start
				}
			}
		}
		return ast.WalkContinue, nil
	}); err != nil {
		return 0, err
	}
	if position == len(source) {
		return 0, fmt.Errorf("top-level %s has no source position", node.Kind())
	}
	if position > len(source) {
		return 0, fmt.Errorf("top-level %s position %d exceeds source length %d", node.Kind(), position, len(source))
	}
	if lineBreak := bytes.LastIndexByte(source[:position], '\n'); lineBreak >= 0 {
		return lineBreak + 1, nil
	}
	return 0, nil
}

// richNormalizedOffsetMap maps every byte boundary in normalized back to the
// corresponding byte boundary in original. normalizeRichListBoundaries only
// inserts line separators; it never deletes or rewrites source bytes.
func richNormalizedOffsetMap(original, normalized string) ([]int, error) {
	offsets := make([]int, len(normalized)+1)
	originalOffset := 0
	for normalizedOffset := 0; normalizedOffset < len(normalized); normalizedOffset++ {
		if originalOffset < len(original) && normalized[normalizedOffset] == original[originalOffset] {
			originalOffset++
		}
		offsets[normalizedOffset+1] = originalOffset
	}
	if originalOffset != len(original) {
		return nil, fmt.Errorf("map rich source normalization: original source is not preserved")
	}
	return offsets, nil
}
