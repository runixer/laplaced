package markdown

// The GFM table parser below is copied from goldmark v2.1.6
// (extension/table.go) with one fix: Transform returns once a table has been
// inserted. The upstream loop keeps scanning a stale copy of the paragraph
// lines, so every later delimiter-like row ("|-|") builds another table from
// all remaining rows. Model output that repeats such rows then parses in
// quadratic time and loses text (github.com/yuin/goldmark/issues/611).
// Delete this file and use extension.TableParser once goldmark ships the fix.
//
// Copyright (c) 2019 Yusuke Inuzuka
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

import (
	"bytes"

	gast "github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

var gfmEscapedPipeCellListKey = parser.NewContextKey()

type gfmEscapedPipeCell struct {
	Cell        *ast.TableCell
	Pos         []int
	Transformed bool
}

func gfmIsTableDelim(bs []byte) bool {
	if w, _ := util.IndentWidth(bs, 0); w > 3 {
		return false
	}
	allSep := true
	for _, b := range bs {
		if b != '-' {
			allSep = false
		}
		if !util.IsSpace(b) && b != '-' && b != '|' && b != ':' {
			return false
		}
	}
	return !allSep
}

func gfmClassifyTableDelim(col []byte) (alignment ast.Alignment, ok bool) {
	col = util.TrimLeftSpace(col)
	col = util.TrimRightSpace(col)
	left := len(col) > 0 && col[0] == ':'
	if left {
		col = col[1:]
	}
	right := len(col) > 0 && col[len(col)-1] == ':'
	if right {
		col = col[:len(col)-1]
	}
	if len(col) == 0 {
		return ast.AlignNone, false
	}
	for _, b := range col {
		if b != '-' {
			return ast.AlignNone, false
		}
	}
	switch {
	case left && right:
		return ast.AlignCenter, true
	case left:
		return ast.AlignLeft, true
	case right:
		return ast.AlignRight, true
	default:
		return ast.AlignNone, true
	}
}

type gfmTableParagraphTransformer struct{}

func (b *gfmTableParagraphTransformer) Transform(node *gast.Paragraph, reader text.Reader, pc parser.Context) {
	ppos := node.Pos()
	lines := node.Source()
	if len(lines) < 2 {
		return
	}
	for i := 1; i < len(lines); i++ {
		alignments := b.parseDelimiter(lines[i], reader)
		if alignments == nil {
			continue
		}
		header := b.parseRow(lines[i-1], alignments, true, reader, pc)
		if header == nil || len(alignments) != header.ChildCount() {
			return
		}
		table := ast.NewTable()
		table.SetPos(ppos)
		tableHeader := ast.NewTableHeader()
		tableHeader.SetPos(header.Pos())
		for c := header.FirstChild(); c != nil; {
			next := c.NextSibling()
			header.RemoveChild(c)
			tableHeader.AppendChild(c)
			c = next
		}
		table.AppendChild(tableHeader)
		var body *ast.TableBody
		for j := i + 1; j < len(lines); j++ {
			if body == nil {
				body = ast.NewTableBody()
				body.SetPos(lines[j].Start)
				table.AppendChild(body)
			}
			body.AppendChild(b.parseRow(lines[j], alignments, false, reader, pc))
		}
		node.SetSource(lines[:i-1])
		node.Parent().InsertAfter(node, table)
		if len(node.Source()) == 0 {
			node.Parent().RemoveChild(node)
			return
		}
		last := node.Source()[i-2]
		last.Stop-- // trim last newline(\n)
		node.Source()[i-2] = last
		// The table took every remaining line: stop here (the upstream fix).
		return
	}
}

func (b *gfmTableParagraphTransformer) parseRow(segment text.Segment,
	alignments []ast.Alignment, isHeader bool, reader text.Reader, pc parser.Context) *ast.TableRow {
	npos := segment
	source := reader.Source()
	segment = segment.TrimLeftSpace(source)
	segment = segment.TrimRightSpace(source)
	line := segment.Bytes(source)
	pos := 0
	limit := len(line)
	row := ast.NewTableRow()
	row.SetPos(npos.Start)
	if len(line) > 0 && line[pos] == '|' {
		pos++
	}
	if len(line) > 0 && line[limit-1] == '|' {
		limit--
	}
	i := 0
	for ; pos < limit; i++ {
		alignment := ast.AlignNone
		if i >= len(alignments) {
			if !isHeader {
				return row
			}
		} else {
			alignment = alignments[i]
		}

		var escapedCell *gfmEscapedPipeCell
		node := ast.NewTableCell(alignment)
		node.SetPos(npos.Start + pos - npos.Padding)
		hasBacktick := false
		closure := pos
		for ; closure < limit; closure++ {
			if line[closure] == '`' {
				hasBacktick = true
			}
			if line[closure] == '|' {
				if closure == 0 || line[closure-1] != '\\' {
					break
				} else if hasBacktick {
					if escapedCell == nil {
						escapedCell = &gfmEscapedPipeCell{node, []int{}, false}
						escapedList := pc.ComputeIfAbsent(gfmEscapedPipeCellListKey,
							func() any {
								return []*gfmEscapedPipeCell{}
							}).([]*gfmEscapedPipeCell)
						escapedList = append(escapedList, escapedCell)
						pc.Set(gfmEscapedPipeCellListKey, escapedList)
					}
					escapedCell.Pos = append(escapedCell.Pos, segment.Start+closure-1)
				}
			}
		}
		seg := text.NewSegment(segment.Start+pos, segment.Start+closure)
		seg = seg.TrimLeftSpace(source)
		seg = seg.TrimRightSpace(source)
		node.AppendSource(seg)
		row.AppendChild(node)
		pos = closure + 1
	}
	for ; i < len(alignments); i++ {
		row.AppendChild(ast.NewTableCell(alignments[i]))
	}
	return row
}

func (b *gfmTableParagraphTransformer) parseDelimiter(segment text.Segment, reader text.Reader) []ast.Alignment {
	line := segment.Bytes(reader.Source())
	if !gfmIsTableDelim(line) {
		return nil
	}
	cols := bytes.Split(line, []byte{'|'})
	if util.IsBlank(cols[0]) {
		cols = cols[1:]
	}
	if len(cols) > 0 && util.IsBlank(cols[len(cols)-1]) {
		cols = cols[:len(cols)-1]
	}

	var alignments []ast.Alignment
	for _, col := range cols {
		alignment, ok := gfmClassifyTableDelim(col)
		if !ok {
			return nil
		}
		alignments = append(alignments, alignment)
	}
	return alignments
}

type gfmTableASTTransformer struct{}

func (a *gfmTableASTTransformer) Transform(_ *gast.Document, reader text.Reader, pc parser.Context) {
	lst := pc.Get(gfmEscapedPipeCellListKey)
	if lst == nil {
		return
	}
	pc.Set(gfmEscapedPipeCellListKey, nil)
	for _, v := range lst.([]*gfmEscapedPipeCell) {
		if v.Transformed {
			continue
		}
		_ = gast.Walk(v.Cell, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
			if !entering || n.Kind() != gast.KindCodeSpan {
				return gast.WalkContinue, nil
			}
			cs := n.(*gast.CodeSpan)
			raw := cs.Value.Bytes(reader.Source())
			fixed := bytes.ReplaceAll(raw, []byte(`\|`), []byte(`|`))
			cs.Value = text.NewMultiLineValueFromString(string(fixed), text.IdentityDecoder)
			v.Transformed = true
			return gast.WalkContinue, nil
		})
	}
}

type gfmTableParserExtension struct{}

func (e *gfmTableParserExtension) ParserOptions(_ *parser.Config) []parser.Option {
	return []parser.Option{
		parser.WithParagraphTransformers(
			util.Prioritized[parser.ParagraphTransformer](&gfmTableParagraphTransformer{}, 200),
		),
		parser.WithASTTransformers(
			util.Prioritized[parser.ASTTransformer](&gfmTableASTTransformer{}, 0),
		),
	}
}
