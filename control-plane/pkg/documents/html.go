package documents

import (
	"context"
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skippedHTML are the elements whose content is not text a reader sees: code, styling, the head
// (apart from the title), and embedded objects.
var skippedHTML = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Svg: true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Canvas: true, atom.Math: true,
	atom.Audio: true, atom.Video: true, atom.Select: true, atom.Datalist: true,
}

var voidHTML = map[atom.Atom]bool{
	atom.Br: true, atom.Hr: true, atom.Img: true, atom.Input: true, atom.Meta: true, atom.Link: true,
	atom.Area: true, atom.Base: true, atom.Col: true, atom.Source: true, atom.Track: true, atom.Wbr: true,
}

var headingLevels = map[atom.Atom]int{atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6}

var blockHTML = map[atom.Atom]bool{
	atom.Address: true, atom.Article: true, atom.Aside: true, atom.Blockquote: true, atom.Body: true,
	atom.Dd: true, atom.Details: true, atom.Div: true, atom.Dl: true, atom.Dt: true, atom.Fieldset: true,
	atom.Figcaption: true, atom.Figure: true, atom.Footer: true, atom.Form: true, atom.Header: true,
	atom.Hr: true, atom.Main: true, atom.Nav: true, atom.Ol: true, atom.P: true, atom.Pre: true,
	atom.Section: true, atom.Summary: true, atom.Ul: true, atom.Html: true, atom.Caption: true,
}

// extractHTML reads HTML with the tokenizer (no tree is built, so memory follows the text kept and
// not the nesting of the input). Scripts, styles, the head, embedded objects and anything hidden by
// the hidden attribute or by display:none and visibility:hidden are dropped: a reader never sees
// them, and hidden text is the usual way to plant instructions for a model. Headings, list items
// and tables keep their structure; links keep their text only.
func extractHTML(ctx context.Context, data []byte, lim Limits) (*Result, error) {
	text, transcoded := decodeText(data)
	res := &Result{}
	if transcoded {
		res.warn("the file is not UTF-8; it was read as Latin-1 or UTF-16 and accented characters may be wrong")
	}
	w := &htmlWalker{c: newCollector(res, lim)}
	z := html.NewTokenizer(strings.NewReader(text))
	z.SetMaxBuf(4 << 20)
	n := 0
	for !w.c.done() {
		if n++; n&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tt := z.Next()
		if tt == html.ErrorToken {
			if err := z.Err(); err != nil && !errors.Is(err, io.EOF) {
				if errors.Is(err, html.ErrBufferExceeded) {
					res.Truncated = true
					res.warn("a single HTML token is larger than 4 MB; the rest was left out")
					break
				}
				return nil, err
			}
			break
		}
		switch tt {
		case html.TextToken:
			w.text(string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			var attrs map[string]string
			if hasAttr {
				attrs = map[string]string{}
				for {
					k, v, more := z.TagAttr()
					attrs[string(k)] = string(v)
					if !more {
						break
					}
				}
			}
			w.start(a, string(name), attrs, tt == html.SelfClosingTagToken)
		case html.EndTagToken:
			name, _ := z.TagName()
			w.end(atom.Lookup(name), string(name))
		}
	}
	w.flushAll()
	return res, nil
}

type htmlTable struct {
	rows [][]string
	row  []string
	cell strings.Builder
	in   bool // a cell is open
}

type htmlWalker struct {
	c *collector

	skipName string // while inside a skipped or hidden element: its tag name
	skipN    int

	buf     strings.Builder // text of the current block
	kind    string          // heading, list_item or paragraph
	level   int
	pre     int
	inTitle bool
	inHead  int
	tables  []*htmlTable
	// extraTables counts tables nested past the supported depth: they are flattened into the
	// innermost supported one, and their end tags must not close it.
	extraTables int
}

func (w *htmlWalker) skipping() bool { return w.skipName != "" }

func (w *htmlWalker) start(a atom.Atom, name string, attrs map[string]string, selfClosing bool) {
	if w.skipping() {
		if name == w.skipName && !selfClosing {
			w.skipN++
		}
		return
	}
	if voidHTML[a] || selfClosing {
		if a == atom.Br {
			w.write("\n")
		}
		return
	}
	if skippedHTML[a] || hiddenHTML(attrs) {
		w.skipName, w.skipN = name, 1
		return
	}
	switch a {
	case atom.Head:
		w.inHead++
	case atom.Title:
		w.inTitle = true
		w.flush()
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.flush()
		w.kind, w.level = "heading", headingLevels[a]
	case atom.Li:
		w.flush()
		w.kind = "list_item"
	case atom.Pre:
		w.flush()
		w.pre++
	case atom.Table:
		w.flush()
		if len(w.tables) < 8 {
			w.tables = append(w.tables, &htmlTable{})
		} else {
			w.extraTables++
		}
	case atom.Tr:
		if t := w.table(); t != nil {
			t.endRow()
		}
	case atom.Td, atom.Th:
		if t := w.table(); t != nil {
			t.endCell()
			t.in = true
		}
	default:
		if blockHTML[a] {
			w.flush()
		}
	}
}

func (w *htmlWalker) end(a atom.Atom, name string) {
	if w.skipping() {
		if name == w.skipName {
			if w.skipN--; w.skipN == 0 {
				w.skipName = ""
			}
		}
		return
	}
	switch a {
	case atom.Head:
		if w.inHead > 0 {
			w.inHead--
		}
	case atom.Title:
		// The title is the only text read from the head; it becomes the document's first heading.
		title := strings.TrimSpace(w.buf.String())
		w.buf.Reset()
		w.inTitle = false
		w.c.text(Block{Kind: "heading", Level: 1, Text: title})
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Li:
		w.flush()
	case atom.Pre:
		w.flush()
		if w.pre > 0 {
			w.pre--
		}
	case atom.Td, atom.Th:
		if t := w.table(); t != nil {
			t.endCell()
		}
	case atom.Tr:
		if t := w.table(); t != nil {
			t.endRow()
		}
	case atom.Table:
		if w.extraTables > 0 {
			w.extraTables--
			return
		}
		w.closeTable()
	default:
		if blockHTML[a] {
			w.flush()
		}
	}
}

func (w *htmlWalker) table() *htmlTable {
	if len(w.tables) == 0 {
		return nil
	}
	return w.tables[len(w.tables)-1]
}

func (t *htmlTable) endCell() {
	if t.in {
		t.row = append(t.row, strings.TrimSpace(t.cell.String()))
		t.cell.Reset()
		t.in = false
	}
}

func (t *htmlTable) endRow() {
	t.endCell()
	if len(t.row) > 0 {
		t.rows = append(t.rows, t.row)
		t.row = nil
	}
}

func (w *htmlWalker) closeTable() {
	t := w.table()
	if t == nil {
		return
	}
	t.endRow()
	w.tables = w.tables[:len(w.tables)-1]
	if len(t.rows) == 0 {
		return
	}
	width := 0
	for _, r := range t.rows {
		if len(r) > width {
			width = len(r)
		}
	}
	for i := range t.rows {
		t.rows[i] = padTo(t.rows[i], width)
	}
	if outer := w.table(); outer != nil && outer.in {
		// A table inside a cell is flattened into the cell's text, a row to a line.
		for _, r := range t.rows {
			outer.cell.WriteString(" " + strings.Join(r, " | "))
		}
		return
	}
	w.c.table(Block{Kind: "table", Table: &Table{Rows: t.rows}})
}

func (w *htmlWalker) text(s string) {
	if w.skipping() || w.inHead > 0 && !w.inTitle {
		return
	}
	s = sanitize(s)
	if t := w.table(); t != nil && t.in {
		t.cell.WriteString(" " + collapse(s) + " ")
		return
	}
	if w.inTitle {
		w.write(collapse(s))
		return
	}
	if w.pre > 0 {
		w.write(s)
		return
	}
	// Whitespace between words is one space; a leading or trailing one is kept as a single space so
	// words in adjacent inline elements do not run together.
	lead := len(s) > 0 && strings.ContainsAny(s[:1], " \t\n\r")
	trail := len(s) > 0 && strings.ContainsAny(s[len(s)-1:], " \t\n\r")
	t := collapse(s)
	if t == "" {
		if lead || trail {
			w.write(" ")
		}
		return
	}
	if lead {
		t = " " + t
	}
	if trail {
		t += " "
	}
	w.write(t)
}

func (w *htmlWalker) write(s string) { w.buf.WriteString(s) }

func (w *htmlWalker) flush() {
	text := strings.TrimSpace(w.buf.String())
	w.buf.Reset()
	if text != "" {
		kind := w.kind
		if kind == "" {
			kind = "paragraph"
		}
		w.c.text(Block{Kind: kind, Level: w.level, Text: text})
	}
	w.kind, w.level = "", 0
}

func (w *htmlWalker) flushAll() {
	for len(w.tables) > 0 {
		w.closeTable()
	}
	w.flush()
}

// hiddenHTML reports whether an element is hidden from a reader by an attribute.
func hiddenHTML(attrs map[string]string) bool {
	if attrs == nil {
		return false
	}
	if _, ok := attrs["hidden"]; ok {
		return true
	}
	style := strings.ToLower(strings.Join(strings.Fields(attrs["style"]), ""))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}
