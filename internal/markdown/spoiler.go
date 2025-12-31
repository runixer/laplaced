package markdown

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// SpoilerNode represents a spoiler node in the AST
type SpoilerNode struct {
	ast.BaseInline
}

// Dump implements ast.Node.Dump
func (n *SpoilerNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// KindSpoiler is the kind of SpoilerNode
var KindSpoiler = ast.NewNodeKind("Spoiler")

// Kind implements ast.Node.Kind
func (n *SpoilerNode) Kind() ast.NodeKind {
	return KindSpoiler
}

// NewSpoilerNode creates a new SpoilerNode
func NewSpoilerNode() *SpoilerNode {
	return &SpoilerNode{}
}

// spoilerParser is an inline parser for spoilers
type spoilerParser struct{}

var defaultSpoilerParser = &spoilerParser{}

// Trigger implements parser.InlineParser.Trigger
func (s *spoilerParser) Trigger() []byte {
	return []byte{'|'}
}

// Parse implements parser.InlineParser.Parse
func (s *spoilerParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, startSegment := block.PeekLine()

	// Check if we have ||
	if len(line) < 2 || line[0] != '|' || line[1] != '|' {
		return nil
	}

	// Find the closing ||
	closingPos := -1
	for i := 2; i < len(line)-1; i++ {
		if line[i] == '|' && line[i+1] == '|' {
			closingPos = i
			break
		}
	}

	if closingPos == -1 {
		// No closing ||, not a spoiler
		return nil
	}

	// Create the spoiler node
	node := NewSpoilerNode()

	// Skip the opening ||
	block.Advance(2)

	// Parse the content between || and ||
	contentLen := closingPos - 2
	if contentLen > 0 {
		// Create a segment for the content
		segment := text.NewSegment(startSegment.Start+2, startSegment.Start+closingPos)
		textNode := ast.NewTextSegment(segment)
		node.AppendChild(node, textNode)
	}

	// Advance past the content and closing ||
	block.Advance(contentLen + 2)

	return node
}

// SpoilerHTMLRenderer renders spoiler nodes to HTML
type SpoilerHTMLRenderer struct {
	html.Config
}

// RegisterFuncs implements renderer.NodeRenderer.RegisterFuncs
func (r *SpoilerHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindSpoiler, r.renderSpoiler)
}

func (r *SpoilerHTMLRenderer) renderSpoiler(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<tg-spoiler>")
	} else {
		_, _ = w.WriteString("</tg-spoiler>")
	}
	return ast.WalkContinue, nil
}

// NewSpoilerHTMLRenderer creates a new SpoilerHTMLRenderer
func NewSpoilerHTMLRenderer(opts ...html.Option) renderer.NodeRenderer {
	r := &SpoilerHTMLRenderer{
		Config: html.NewConfig(),
	}
	for _, opt := range opts {
		opt.SetHTMLOption(&r.Config)
	}
	return r
}

// SpoilerExtension is a goldmark extension for spoilers
type SpoilerExtension struct{}

// Extend implements goldmark.Extender
func (e *SpoilerExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(defaultSpoilerParser, 500),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(NewSpoilerHTMLRenderer(), 500),
	))
}

// NewSpoilerExtension creates a new SpoilerExtension
func NewSpoilerExtension() goldmark.Extender {
	return &SpoilerExtension{}
}
