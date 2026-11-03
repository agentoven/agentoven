package documents_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// zipOf builds a zip from name/content pairs, in order.
func zipOf(t testing.TB, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	contentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`
	nsW          = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"`
)

func docxPackage(t testing.TB, body string, extra ...[2]string) []byte {
	t.Helper()
	files := [][2]string{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/styles.xml", `<w:styles ` + nsW + `><w:style w:styleId="H1x"><w:name w:val="heading 1"/></w:style><w:style w:styleId="Ttl"><w:name w:val="Title"/></w:style></w:styles>`},
		{"word/document.xml", `<?xml version="1.0"?><w:document ` + nsW + `><w:body>` + body + `</w:body></w:document>`},
	}
	return zipOf(t, append(files, extra...)...)
}

func wp(text string) string { return `<w:p><w:r><w:t>` + text + `</w:t></w:r></w:p>` }

func wcell(text string) string { return `<w:tc>` + wp(text) + `</w:tc>` }

func wrow(cells ...string) string {
	var b strings.Builder
	b.WriteString(`<w:tr>`)
	for _, c := range cells {
		b.WriteString(wcell(c))
	}
	b.WriteString(`</w:tr>`)
	return b.String()
}

// xlsxPackage builds a workbook. sheets maps a sheet name to its worksheet XML (the <sheetData> content
// is given by the caller); hidden names the sheets with state="hidden".
func xlsxPackage(t testing.TB, sheets [][2]string, opts xlsxOpts) []byte {
	t.Helper()
	var wb, rels strings.Builder
	wb.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	if opts.date1904 {
		wb.WriteString(`<workbookPr date1904="1"/>`)
	}
	wb.WriteString(`<sheets>`)
	rels.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	files := [][2]string{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
	}
	for i, s := range sheets {
		state := ""
		if opts.hidden[s[0]] {
			state = ` state="hidden"`
		}
		fmt.Fprintf(&wb, `<sheet name=%q sheetId="%d" r:id="rId%d"%s/>`, s[0], i+1, i+1, state)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		files = append(files, [2]string{fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + s[1] + `</sheetData></worksheet>`})
	}
	wb.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	files = append(files, [2]string{"xl/workbook.xml", wb.String()}, [2]string{"xl/_rels/workbook.xml.rels", rels.String()})
	if opts.shared != "" {
		files = append(files, [2]string{"xl/sharedStrings.xml", `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` + opts.shared + `</sst>`})
	}
	if opts.styles != "" {
		files = append(files, [2]string{"xl/styles.xml", `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` + opts.styles + `</styleSheet>`})
	}
	return zipOf(t, files...)
}

type xlsxOpts struct {
	shared, styles string
	hidden         map[string]bool
	date1904       bool
}

// patchSizes rewrites the uncompressed size recorded for one entry, in both its local and central
// headers, so that the archive lies about how far the entry expands.
func patchSizes(data []byte, name string, size uint32) []byte {
	out := append([]byte{}, data...)
	put := func(at int) {
		out[at], out[at+1], out[at+2], out[at+3] = byte(size), byte(size>>8), byte(size>>16), byte(size>>24)
	}
	for i := 0; i+46 < len(out); i++ {
		switch {
		case bytes.Equal(out[i:i+4], []byte("PK\x03\x04")) && bytes.HasPrefix(out[i+30:], []byte(name)):
			put(i + 22)
		case bytes.Equal(out[i:i+4], []byte("PK\x01\x02")) && bytes.HasPrefix(out[i+46:], []byte(name)):
			put(i + 24)
		}
	}
	return out
}
