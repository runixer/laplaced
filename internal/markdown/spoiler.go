package markdown

import (
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// SpoilerNode represents a spoiler node in the AST
type SpoilerNode struct {
	ast.BaseInline
}

// Dump implements ast.Node.Dump
func (n *SpoilerNode) Dump(_ []byte) *ast.NodeDump {
	return ast.NewNodeDump(n, nil)
}

// KindSpoiler is the kind of SpoilerNode
var KindSpoiler = ast.NewNodeKind("Spoiler")

// Kind implements ast.Node.Kind
func (n *SpoilerNode) Kind() ast.NodeKind {
	return KindSpoiler
}

// NewSpoilerNode creates a new SpoilerNode
func NewSpoilerNode() *SpoilerNode {
	n := &SpoilerNode{}
	n.Init(n)
	return n
}

// spoilerParser is an inline parser for spoilers
type spoilerParser struct{}

var defaultSpoilerParser = &spoilerParser{}

// Trigger implements parser.InlineParser.Trigger
func (s *spoilerParser) Trigger() []byte {
	return []byte{'|'}
}

// Parse implements parser.InlineParser.Parse
func (s *spoilerParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
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

	// The content between || and || stays literal text: it is not parsed as
	// Markdown, but escapes and entities are decoded like any other text.
	contentLen := closingPos - 2
	if contentLen > 0 {
		segment := text.NewSegment(startSegment.Start+2, startSegment.Start+closingPos)
		node.AppendChild(ast.NewText(text.NewSingleLineValueFromSegment(segment, block.Decoder())))
	}

	// Advance past the content and closing ||
	block.Advance(contentLen + 2)

	return node
}

// spoilerInlineParser is the spoiler parser registration shared by the
// legacy and rich renderers.
func spoilerInlineParser(priority int) util.PrioritizedValue[parser.InlineParser] {
	return util.Prioritized[parser.InlineParser](defaultSpoilerParser, priority)
}
