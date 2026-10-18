package markdown

import (
	"bytes"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/util"
)

// renderConfig is the goldmark renderer configuration shared by the Telegram
// renderers. It deliberately has no default node renderers: every kind the
// Telegram output supports is registered explicitly, and any other kind is
// walked through so only its children render. Nothing can fall back to
// goldmark's stock HTML output.
type renderConfig struct {
	renderer.Config[util.BufWriter, renderConfig]
}

type nodeRenderers = map[ast.NodeKind]renderer.NodeRenderer[util.BufWriter]

// adapt wraps a renderer method that does not need the render context.
func adapt(f func(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error)) renderer.NodeRenderer[util.BufWriter] {
	return renderer.NodeRendererFunc(func(w util.BufWriter, source []byte, n ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
		return f(w, source, n, entering)
	})
}

// newMarkdownParser returns a GFM parser with the given extra inline parsers.
// It assembles extension.GFMParser by hand to use the patched table parser
// from gfm_table.go.
func newMarkdownParser(inlineParsers ...util.PrioritizedValue[parser.InlineParser]) parser.Parser {
	return parser.New(
		parser.WithExtensions(
			extension.NewLinkifyParser(),
			&gfmTableParserExtension{},
			extension.NewStrikethroughParser(),
			extension.NewTaskListItemParser(),
		),
		parser.WithInlineParsers(inlineParsers...),
	)
}

// renderNode renders node with the given node renderers and returns the output.
func renderNode(source []byte, node ast.Node, renderers nodeRenderers) (string, error) {
	var hb renderer.HelperBuilder[util.BufWriter, renderConfig]
	r := hb.Options(renderer.WithNodeRenderers[util.BufWriter, renderConfig](renderers)).Build()
	var out bytes.Buffer
	if err := r.Render(util.NewErrorBufWriter(&out), source, node); err != nil {
		return "", err
	}
	return out.String(), nil
}
