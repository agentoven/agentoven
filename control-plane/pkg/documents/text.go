package documents

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeText turns the bytes of a text file into UTF-8. A byte-order mark picks UTF-8 or UTF-16 (the
// form Excel's "Unicode text" export uses); bytes that are not valid UTF-8 are read as Latin-1,
// which is the usual encoding of such a file and never fails. The second result says the file was
// not UTF-8, so the caller can warn that accented characters may be wrong.
func decodeText(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		return decodeUTF16(data), true
	}
	if utf8.Valid(data) {
		return string(data), false
	}
	runes := make([]rune, len(data))
	for i, c := range data {
		runes[i] = rune(c)
	}
	return string(runes), true
}

func decodeUTF16(data []byte) string {
	bigEndian := data[0] == 0xfe
	data = data[2:]
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			units = append(units, uint16(data[i+1])<<8|uint16(data[i]))
		}
	}
	return string(utf16.Decode(units))
}

func extractPlain(data []byte, lim Limits) (*Result, error) {
	res := &Result{}
	text, transcoded := decodeText(data)
	if transcoded {
		res.warn("the file is not UTF-8; it was read as %s and accented characters may be wrong", "Latin-1 or UTF-16")
	}
	c := newCollector(res, lim)
	text = strings.ReplaceAll(sanitize(text), "\n\n\n", "\n\n")
	c.text(Block{Kind: "paragraph", Text: text})
	return res, nil
}

func extractDelimited(f Format, data []byte, lim Limits) (*Result, error) {
	res := &Result{}
	text, transcoded := decodeText(data)
	if transcoded {
		res.warn("the file is not UTF-8; it was read as %s and accented characters may be wrong", "Latin-1 or UTF-16")
	}
	text = sanitize(text)
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delimiter(f, text)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var rows [][]string
	cells := 0
	width := 0
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A ragged or badly quoted line is skipped, not fatal: the rest of the file is still useful.
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				res.warn("line %d could not be read as a record (%v) and was skipped", pe.Line, pe.Err)
				continue
			}
			return nil, err
		}
		if len(rec) > lim.MaxCols {
			rec = rec[:lim.MaxCols]
			if !res.Truncated {
				res.warn("rows were cut to the first %d columns", lim.MaxCols)
			}
		}
		if cells += len(rec); cells > lim.MaxCells {
			res.Truncated = true
			res.warn("the file has more than %d cells; the rest was left out", lim.MaxCells)
			break
		}
		if len(rec) > width {
			width = len(rec)
		}
		rows = append(rows, rec)
	}
	for i := range rows {
		rows[i] = padTo(rows[i], width)
	}
	c := newCollector(res, lim)
	if len(rows) > 0 {
		c.table(Block{Kind: "table", Table: &Table{Rows: rows}})
	}
	return res, nil
}

// delimiter is the field separator: tab for a TSV, and for a CSV whichever of comma, semicolon, tab
// or pipe is most frequent on the first line (spreadsheet exports in many locales use a semicolon).
func delimiter(f Format, text string) rune {
	if f == FormatTSV {
		return '\t'
	}
	line, _, _ := strings.Cut(text, "\n")
	best, bestN := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if n := strings.Count(line, string(d)); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}
