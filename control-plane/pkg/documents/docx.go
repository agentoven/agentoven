package documents

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// errEnough ends a read early, without an error, once the text budget is spent.
var errEnough = errors.New("enough")

// extractDOCX reads the body of a Word document: paragraphs, headings (by style name or outline
// level), list items, tables, and the paragraphs of text boxes. What Word shows a reader is what
// is read: deleted tracked changes (w:del) and the fallback copy of a text box are skipped, and so is
// text marked hidden (w:vanish), which a person never sees but a model would. Headers, footers,
// footnotes, comments and images are not read (see ADR-0039).
func extractDOCX(ctx context.Context, data []byte, lim Limits) (*Result, error) {
	pkg, err := openPackage(data, lim)
	if err != nil {
		return nil, err
	}
	main, err := pkg.mainPart(ctx, "/officeDocument")
	if err != nil {
		return nil, err
	}
	styles, err := loadStyleNames(ctx, pkg, partDir(main)+"/styles.xml")
	if err != nil {
		return nil, err
	}
	rc, err := pkg.open(main)
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return nil, fmt.Errorf("documents: not a valid docx: the main part %s is missing", main)
	}
	defer rc.Close()

	res := &Result{}
	w := &docxWalker{x: newXML(ctx, rc, lim.MaxXMLDepth), styles: styles, c: newCollector(res, lim)}
	w.emit = func(p docxPara) { w.c.text(Block{Kind: p.kind(), Level: p.level, Text: p.text}) }
	for {
		tok, err := w.x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("documents: reading %s: %w", main, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var rerr error
		switch se.Name.Local {
		case "p":
			rerr = w.paragraph(w.emit)
		case "tbl":
			var rows [][]string
			if rows, rerr = w.table(); rerr == nil {
				w.c.table(Block{Kind: "table", Table: &Table{Rows: rows}})
			}
		case "del", "moveFrom", "Fallback":
			rerr = w.x.skip()
		}
		if rerr == nil && w.c.done() {
			rerr = errEnough
		}
		if errors.Is(rerr, errEnough) {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("documents: reading %s: %w", main, rerr)
		}
	}
	return res, nil
}

func partDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

type docxWalker struct {
	x      *xmlStream
	styles map[string]string // style id -> lower-case style name
	c      *collector
	emit   func(docxPara)
}

type docxPara struct {
	text  string
	level int // heading level, 0 for a body paragraph
	list  bool
}

func (p docxPara) kind() string {
	switch {
	case p.level > 0:
		return "heading"
	case p.list:
		return "list_item"
	}
	return "paragraph"
}

// paragraph reads one w:p (its start was just read) and hands the paragraph to sink. A paragraph
// inside it (a text box) is handed to the same sink first.
func (w *docxWalker) paragraph(sink func(docxPara)) error {
	var (
		b     strings.Builder
		para  docxPara
		style string
		depth = 1
	)
	for depth > 0 {
		tok, err := w.x.next()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "pPr":
				style, para.level, para.list, err = w.paragraphProps()
				para.level = w.headingLevel(style, para.level)
			case "r":
				var s string
				if s, err = w.run(sink); err == nil {
					b.WriteString(s)
				}
			case "p":
				err = w.paragraph(sink)
			case "del", "moveFrom", "Fallback":
				err = w.x.skip()
			default:
				depth++
			}
			if err != nil {
				return err
			}
		case xml.EndElement:
			depth--
		}
	}
	para.text = sanitize(b.String())
	if strings.TrimSpace(para.text) != "" {
		sink(para)
	}
	if w.c.done() {
		return errEnough
	}
	return nil
}

// paragraphProps reads w:pPr: the style id, the outline level and whether the paragraph is numbered.
func (w *docxWalker) paragraphProps() (style string, outline int, list bool, err error) {
	for depth := 1; depth > 0; {
		tok, e := w.x.next()
		if e != nil {
			return "", 0, false, e
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "pStyle":
				style = attr(t, "val")
			case "outlineLvl":
				if n, e := strconv.Atoi(attr(t, "val")); e == nil && n >= 0 && n < 9 {
					outline = n + 1
				}
			case "numId":
				list = attr(t, "val") != "0"
			case "rPr":
				// The paragraph mark's own run properties say nothing about the text.
				if e := w.x.skip(); e != nil {
					return "", 0, false, e
				}
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}
	return style, outline, list, nil
}

// headingLevel is the heading level a style (by name) or an outline level gives a paragraph.
func (w *docxWalker) headingLevel(style string, outline int) int {
	name := w.styles[style]
	if name == "" {
		name = strings.ToLower(style)
	}
	name = strings.ReplaceAll(name, " ", "")
	switch {
	case name == "title":
		return 1
	case name == "subtitle":
		return 2
	case strings.HasPrefix(name, "heading"):
		if n, err := strconv.Atoi(strings.TrimPrefix(name, "heading")); err == nil && n >= 1 && n <= 9 {
			if n > 6 {
				n = 6
			}
			return n
		}
	}
	if outline > 6 {
		outline = 6
	}
	return outline
}

// run reads one w:r: its text, tabs and line breaks, unless the run is hidden. A text box inside it
// has paragraphs of its own, which go to sink.
func (w *docxWalker) run(sink func(docxPara)) (string, error) {
	var (
		b      strings.Builder
		hidden bool
		depth  = 1
	)
	for depth > 0 {
		tok, err := w.x.next()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "vanish", "webHidden":
				hidden = hidden || isOn(attr(t, "val"))
				depth++
			case "t":
				var s string
				if s, err = w.x.text(); err == nil {
					b.WriteString(s)
				}
			case "tab":
				b.WriteByte('\t')
				depth++
			case "br", "cr":
				b.WriteByte('\n')
				depth++
			case "noBreakHyphen":
				b.WriteByte('-')
				depth++
			case "p":
				err = w.paragraph(sink)
			case "Fallback", "del":
				err = w.x.skip()
			default:
				depth++
			}
			if err != nil {
				return "", err
			}
		case xml.EndElement:
			depth--
		}
	}
	if hidden {
		return "", nil
	}
	return b.String(), nil
}

func isOn(v string) bool { return v == "" || v == "1" || v == "true" || v == "on" }

// table reads one w:tbl (its start was just read) into rows of cell text. A cell's paragraphs are
// joined with newlines; a table inside a cell is flattened into its lines; a merged cell is followed
// by empty cells so that columns stay aligned.
func (w *docxWalker) table() ([][]string, error) {
	var rows [][]string
	for depth := 1; depth > 0; {
		tok, err := w.x.next()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "tr" {
				row, err := w.tableRow()
				if err != nil {
					return nil, err
				}
				rows = append(rows, row)
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if width > 1024 {
		return nil, fmt.Errorf("%w: a table is %d columns wide", ErrTooLarge, width)
	}
	for i := range rows {
		rows[i] = padTo(rows[i], width)
	}
	return rows, nil
}

func (w *docxWalker) tableRow() ([]string, error) {
	var row []string
	for depth := 1; depth > 0; {
		tok, err := w.x.next()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "tc" {
				cell, span, err := w.tableCell()
				if err != nil {
					return nil, err
				}
				row = append(row, cell)
				for i := 1; i < span && i < 64; i++ {
					row = append(row, "")
				}
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return row, nil
}

func (w *docxWalker) tableCell() (string, int, error) {
	var (
		lines []string
		span  = 1
	)
	sink := func(p docxPara) { lines = append(lines, strings.TrimSpace(p.text)) }
	for depth := 1; depth > 0; {
		tok, err := w.x.next()
		if err != nil {
			return "", 0, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "gridSpan":
				if n, e := strconv.Atoi(attr(t, "val")); e == nil && n > 1 {
					span = n
				}
				depth++
			case "p":
				if err := w.paragraph(sink); err != nil && !errors.Is(err, errEnough) {
					return "", 0, err
				}
			case "tbl":
				inner, err := w.table()
				if err != nil {
					return "", 0, err
				}
				for _, r := range inner {
					lines = append(lines, strings.Join(r, " | "))
				}
			case "del", "moveFrom", "Fallback":
				if err := w.x.skip(); err != nil {
					return "", 0, err
				}
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return strings.Join(lines, "\n"), span, nil
}

// loadStyleNames maps style ids to their lower-case names, so that a heading is found by what the
// style is called ("heading 2") and not by an id that a template may have renamed.
func loadStyleNames(ctx context.Context, pkg *ooxmlPackage, part string) (map[string]string, error) {
	rc, err := pkg.open(part)
	if err != nil || rc == nil {
		return map[string]string{}, err
	}
	defer rc.Close()
	x := newXML(ctx, rc, pkg.lim.MaxXMLDepth)
	names := map[string]string{}
	var id string
	for {
		tok, err := x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return names, nil
			}
			return nil, fmt.Errorf("documents: reading %s: %w", part, err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case "style":
				id = attr(se, "styleId")
			case "name":
				if id != "" && len(names) < 10_000 {
					names[id] = strings.ToLower(attr(se, "val"))
				}
			}
		}
	}
}
