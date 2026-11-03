package documents

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// extractXLSX reads a workbook: each visible worksheet becomes a table whose columns are lettered
// and whose rows carry their worksheet row number, so a reader can cite a cell as "C7". Values are
// what Excel last calculated (a formula's cached result); shared strings, inline strings, booleans,
// errors and dates (by the cell's number format) are resolved. Hidden sheets are skipped, with a
// warning, because a person never sees them and a model would. Charts, images, comments, merged
// ranges and defined names are not read (see ADR-0039).
func extractXLSX(ctx context.Context, data []byte, lim Limits) (*Result, error) {
	pkg, err := openPackage(data, lim)
	if err != nil {
		return nil, err
	}
	main, err := pkg.mainPart(ctx, "/officeDocument")
	if err != nil {
		return nil, err
	}
	dir := partDir(main)
	wb, err := readWorkbook(ctx, pkg, main)
	if err != nil {
		return nil, err
	}
	rels, err := pkg.relationships(ctx, partDir(main)+"/_rels/"+main[strings.LastIndexByte(main, '/')+1:]+".rels")
	if err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, r := range rels {
		if r.Mode != "External" {
			targets[r.ID] = resolveTarget(dir, r.Target)
		}
	}
	shared, err := readSharedStrings(ctx, pkg, dir+"/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	dates, err := readDateStyles(ctx, pkg, dir+"/styles.xml")
	if err != nil {
		return nil, err
	}

	res := &Result{}
	c := newCollector(res, lim)
	cells := 0
	for i, sh := range wb.sheets {
		if i >= lim.MaxSheets {
			res.Truncated = true
			res.warn("the workbook has more than %d sheets; the rest were left out", lim.MaxSheets)
			break
		}
		if sh.hidden {
			res.warn("sheet %q is hidden and was not read", sh.name)
			continue
		}
		if cells >= lim.MaxCells {
			res.Truncated = true
			res.warn("the workbook has more than %d cells; sheet %q and the ones after it were left out", lim.MaxCells, sh.name)
			break
		}
		res.Sheets = append(res.Sheets, sh.name)
		part := targets[sh.rid]
		if part == "" {
			res.warn("sheet %q has no part in the file and was not read", sh.name)
			continue
		}
		rc, err := pkg.open(part)
		if err != nil {
			return nil, err
		}
		if rc == nil {
			res.warn("sheet %q is declared but its part %s is missing", sh.name, part)
			continue
		}
		t, err := readSheet(ctx, rc, lim, &sheetReader{shared: shared, dates: dates, date1904: wb.date1904, budget: lim.MaxCells - cells})
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("documents: reading sheet %q: %w", sh.name, err)
		}
		cells += t.cells
		if t.table != nil {
			c.table(Block{Kind: "table", Sheet: sh.name, Table: t.table})
		}
		if t.cut != "" {
			res.Truncated = true
			res.warn("sheet %q: %s", sh.name, t.cut)
		}
		if c.done() {
			break
		}
	}
	return res, nil
}

type workbook struct {
	sheets   []sheetRef
	date1904 bool
}

type sheetRef struct {
	name, rid string
	hidden    bool
}

func readWorkbook(ctx context.Context, pkg *ooxmlPackage, part string) (*workbook, error) {
	rc, err := pkg.open(part)
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return nil, fmt.Errorf("documents: not a valid xlsx: the workbook part %s is missing", part)
	}
	defer rc.Close()
	x := newXML(ctx, rc, pkg.lim.MaxXMLDepth)
	wb := &workbook{}
	for {
		tok, err := x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return wb, nil
			}
			return nil, fmt.Errorf("documents: reading %s: %w", part, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "workbookPr":
			wb.date1904 = attr(se, "date1904") == "1" || attr(se, "date1904") == "true"
		case "sheet":
			wb.sheets = append(wb.sheets, sheetRef{name: attr(se, "name"), rid: attr(se, "id"), hidden: attr(se, "state") == "hidden" || attr(se, "state") == "veryHidden"})
		}
	}
}

// readSharedStrings reads the shared string table: <si> items whose text is the concatenation of
// their <t> runs (phonetic runs, <rPh>, are not part of the text). The table is bounded by the part's
// limits, and its entry count by the cell budget, since a cell can only name an entry.
func readSharedStrings(ctx context.Context, pkg *ooxmlPackage, part string) ([]string, error) {
	rc, err := pkg.open(part)
	if err != nil || rc == nil {
		return nil, err
	}
	defer rc.Close()
	x := newXML(ctx, rc, pkg.lim.MaxXMLDepth)
	var out []string
	for {
		tok, err := x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("documents: reading %s: %w", part, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "si" {
			continue
		}
		s, err := readRichText(x)
		if err != nil {
			return nil, fmt.Errorf("documents: reading %s: %w", part, err)
		}
		if len(out) >= pkg.lim.MaxCells {
			return nil, fmt.Errorf("%w: more than %d shared strings", ErrTooLarge, pkg.lim.MaxCells)
		}
		out = append(out, s)
	}
}

// readRichText reads an <si> or <is> element (its start was just read): the text of its <t> children,
// including those inside rich-text runs, without phonetic guides.
func readRichText(x *xmlStream) (string, error) {
	var b strings.Builder
	for depth := 1; depth > 0; {
		tok, err := x.next()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				s, err := x.text()
				if err != nil {
					return "", err
				}
				b.WriteString(s)
			case "rPh":
				if err := x.skip(); err != nil {
					return "", err
				}
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return b.String(), nil
}

// readDateStyles returns, for each cell style index, whether its number format shows a date or a
// time, so that a serial number such as 45123 is shown as 2023-07-14 and not as a bare number.
func readDateStyles(ctx context.Context, pkg *ooxmlPackage, part string) ([]bool, error) {
	rc, err := pkg.open(part)
	if err != nil || rc == nil {
		return nil, err
	}
	defer rc.Close()
	x := newXML(ctx, rc, pkg.lim.MaxXMLDepth)
	custom := map[int]string{}
	var xfs []bool
	inCellXfs := false
	for {
		tok, err := x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return xfs, nil
			}
			return nil, fmt.Errorf("documents: reading %s: %w", part, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmt":
				if id, err := strconv.Atoi(attr(t, "numFmtId")); err == nil && len(custom) < 5000 {
					custom[id] = attr(t, "formatCode")
				}
			case "cellXfs":
				inCellXfs = true
			case "xf":
				if inCellXfs && len(xfs) < 100_000 {
					id, _ := strconv.Atoi(attr(t, "numFmtId"))
					xfs = append(xfs, isDateFormat(id, custom[id]))
				}
			}
		case xml.EndElement:
			if t.Name.Local == "cellXfs" {
				inCellXfs = false
			}
		}
	}
}

// isDateFormat: the built-in date and time formats are ids 14-22 and 45-47; a custom one is a date
// when its code, outside quoted text and brackets, uses a date or time letter.
func isDateFormat(id int, code string) bool {
	if (id >= 14 && id <= 22) || (id >= 45 && id <= 47) {
		return true
	}
	if code == "" {
		return false
	}
	inQuote, inBracket := false, false
	for i := 0; i < len(code); i++ {
		switch ch := code[i]; {
		case ch == '"':
			inQuote = !inQuote
		case inQuote:
		case ch == '[':
			inBracket = true
		case ch == ']':
			inBracket = false
		case inBracket:
		case ch == '\\' || ch == '_' || ch == '*':
			i++
		case strings.ContainsRune("dmyhsDMYHS", rune(ch)):
			return true
		}
	}
	return false
}

type sheetReader struct {
	shared   []string
	dates    []bool
	date1904 bool
	budget   int // cells this sheet may still read
}

type sheetData struct {
	table *Table
	cells int
	cut   string // why the sheet was cut short, empty if it was read whole
}

type cellRow struct {
	n     int
	cells map[int]string
}

// readSheet streams a worksheet part into a table. Rows are kept sparsely (only those with a value),
// so a sheet that claims a million rows costs only the rows it fills.
func readSheet(ctx context.Context, r io.Reader, lim Limits, sr *sheetReader) (*sheetData, error) {
	x := newXML(ctx, r, lim.MaxXMLDepth)
	var (
		rows   []cellRow
		cur    *cellRow
		rowNo  int
		colNo  int
		cells  int
		minC   = 1 << 30
		maxC   int
		out    = &sheetData{}
		colCut bool
	)
loop:
	for {
		tok, err := x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				if n, err := strconv.Atoi(attr(t, "r")); err == nil && n > 0 {
					rowNo = n
				} else {
					rowNo++
				}
				colNo = 0
				rows = append(rows, cellRow{n: rowNo, cells: map[int]string{}})
				cur = &rows[len(rows)-1]
			case "c":
				if cur == nil {
					if err := x.skip(); err != nil {
						return nil, err
					}
					continue
				}
				if col, ok := columnOf(attr(t, "r")); ok {
					colNo = col
				} else {
					colNo++
				}
				if cells++; cells > sr.budget {
					out.cut = fmt.Sprintf("the workbook has more than %d cells; the rest of this sheet was left out", lim.MaxCells)
					break loop
				}
				val, formula, err := sr.cell(x, t)
				if err != nil {
					return nil, err
				}
				if val == "" && formula != "" {
					val = "=" + formula // a formula Excel never calculated: show the formula, marked as one
				}
				val = sanitize(val)
				if strings.TrimSpace(val) == "" {
					continue
				}
				if colNo > lim.MaxCols {
					colCut = true
					continue
				}
				cur.cells[colNo] = val
				if colNo < minC {
					minC = colNo
				}
				if colNo > maxC {
					maxC = colNo
				}
			}
		}
	}
	out.cells = cells
	if colCut {
		out.cut += fmt.Sprintf("columns past %s were left out", columnName(lim.MaxCols))
	}
	// Drop the rows with no value; what is left is the used range.
	t := &Table{}
	firstRow, lastRow := 0, 0
	for _, row := range rows {
		if len(row.cells) == 0 {
			continue
		}
		if firstRow == 0 {
			firstRow = row.n
		}
		lastRow = row.n
		t.RowNumbers = append(t.RowNumbers, row.n)
	}
	if len(t.RowNumbers) == 0 {
		return out, nil
	}
	for c := minC; c <= maxC; c++ {
		t.Columns = append(t.Columns, columnName(c))
	}
	for _, row := range rows {
		if len(row.cells) == 0 {
			continue
		}
		line := make([]string, maxC-minC+1)
		for col, v := range row.cells {
			line[col-minC] = v
		}
		t.Rows = append(t.Rows, line)
	}
	// Rows can be out of order in a hand-made file; the table is read top to bottom.
	sort.SliceStable(t.Rows, func(i, j int) bool { return t.RowNumbers[i] < t.RowNumbers[j] })
	sort.Ints(t.RowNumbers)
	t.Range = columnName(minC) + strconv.Itoa(firstRow) + ":" + columnName(maxC) + strconv.Itoa(lastRow)
	out.table = t
	return out, nil
}

// cell reads one <c> (its start was just read) and returns its displayed value and its formula.
func (sr *sheetReader) cell(x *xmlStream, c xml.StartElement) (value, formula string, err error) {
	typ, style := attr(c, "t"), attr(c, "s")
	var raw, inline string
	for depth := 1; depth > 0; {
		tok, err := x.next()
		if err != nil {
			return "", "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "v":
				if raw, err = x.text(); err != nil {
					return "", "", err
				}
			case "f":
				if formula, err = x.text(); err != nil {
					return "", "", err
				}
			case "is":
				if inline, err = readRichText(x); err != nil {
					return "", "", err
				}
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	raw = strings.TrimSpace(raw)
	switch typ {
	case "s":
		if i, err := strconv.Atoi(raw); err == nil && i >= 0 && i < len(sr.shared) {
			return sr.shared[i], formula, nil
		}
		return "", formula, nil
	case "inlineStr":
		return inline, formula, nil
	case "b":
		if raw == "1" {
			return "TRUE", formula, nil
		}
		return "FALSE", formula, nil
	case "str", "e", "d":
		return raw, formula, nil
	}
	if raw == "" {
		return "", formula, nil
	}
	if si, err := strconv.Atoi(style); err == nil && si >= 0 && si < len(sr.dates) && sr.dates[si] {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			if s, ok := excelDate(f, sr.date1904); ok {
				return s, formula, nil
			}
		}
	}
	return raw, formula, nil
}

// excelDate converts a serial day number to an ISO date, with the time when there is a fraction.
// Excel's 1900 system counts a leap day that did not exist, which the 1899-12-30 epoch absorbs for
// every date after February 1900.
func excelDate(serial float64, date1904 bool) (string, bool) {
	if serial < 0 || serial > 2_958_465 { // 9999-12-31
		return "", false
	}
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	days := int(serial)
	t := epoch.AddDate(0, 0, days)
	frac := serial - float64(days)
	if frac < 1e-9 {
		return t.Format("2006-01-02"), true
	}
	t = t.Add(time.Duration(frac*float64(24*time.Hour) + 0.5*float64(time.Second)).Truncate(time.Second))
	if days == 0 {
		return t.Format("15:04:05"), true
	}
	return t.Format("2006-01-02 15:04:05"), true
}

// columnOf parses the column of a cell reference such as "BC12" into 1-based form.
func columnOf(ref string) (int, bool) {
	n := 0
	i := 0
	for ; i < len(ref); i++ {
		ch := ref[i]
		if ch >= 'a' && ch <= 'z' {
			ch -= 32
		}
		if ch < 'A' || ch > 'Z' {
			break
		}
		if n = n*26 + int(ch-'A'+1); n > 16384 {
			return 0, false
		}
	}
	return n, i > 0 && n > 0
}

func columnName(n int) string {
	var b []byte
	for ; n > 0; n = (n - 1) / 26 {
		b = append([]byte{byte('A' + (n-1)%26)}, b...)
	}
	return string(b)
}
