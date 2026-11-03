package documents_test

import (
	"context"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/documents"
)

// The readers take bytes from anyone who can upload a file, so the property under test is the same
// for every format: whatever the input, Extract returns (it neither panics nor runs long) and a
// success respects the text limit. Run longer with: go test -fuzz FuzzDOCXPart -fuzztime 5m

func fuzzLimits() documents.Limits {
	return documents.Limits{MaxTextBytes: 20_000, MaxCells: 5_000, MaxPartBytes: 4 << 20}
}

func checkExtract(t *testing.T, f documents.Format, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := documents.Extract(ctx, f, data, fuzzLimits())
	if err != nil {
		return
	}
	if n := len(res.Text(0)); n > 3*fuzzLimits().MaxTextBytes {
		t.Fatalf("%s: %d bytes of text for a %d byte limit", f, n, fuzzLimits().MaxTextBytes)
	}
}

func FuzzDOCXPart(f *testing.F) {
	f.Add(`<w:p><w:pPr><w:pStyle w:val="H1x"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p/></w:tc></w:tr></w:tbl>`)
	f.Add(`<w:sdt><w:p><w:r><mc:AlternateContent><mc:Choice><w:txbxContent><w:p/></w:txbxContent></mc:Choice></mc:AlternateContent></w:r></w:p></w:sdt>`)
	f.Fuzz(func(t *testing.T, body string) {
		checkExtract(t, documents.FormatDOCX, docxPackage(t, body))
	})
}

func FuzzXLSXSheet(f *testing.F) {
	f.Add(`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C3" s="1"><v>45123.5</v></c></row><row><c t="inlineStr"><is><t>x</t></is></c></row>`)
	f.Add(`<row r="2"><c r="ZZ2" t="b"><v>1</v></c><c><f>1+1</f></c></row>`)
	f.Fuzz(func(t *testing.T, sheet string) {
		data := xlsxPackage(t, [][2]string{{"S", sheet}}, xlsxOpts{
			shared: `<si><t>a</t></si>`, styles: `<numFmts count="1"><numFmt numFmtId="164" formatCode="yyyy"/></numFmts><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="164"/></cellXfs>`,
		})
		checkExtract(t, documents.FormatXLSX, data)
	})
}

func FuzzHTML(f *testing.F) {
	f.Add(`<html><head><title>t</title><script>x</script></head><body><h1>a</h1><table><tr><td>1<td>2</table><div hidden>x</div></body>`)
	f.Add(`<p><b>unclosed<table><tr><td><table><tr><td>`)
	f.Fuzz(func(t *testing.T, page string) { checkExtract(t, documents.FormatHTML, []byte(page)) })
}

func FuzzRTF(f *testing.F) {
	f.Add(`{\rtf1\ansi{\fonttbl{\f0 x;}}\pard a\'e9 荤? \par}`)
	f.Add(`{\rtf1{\*\x y}{\info{\a b}}\uc0\u65 \cell\row}`)
	f.Fuzz(func(t *testing.T, s string) { checkExtract(t, documents.FormatRTF, []byte(s)) })
}

func FuzzDelimited(f *testing.F) {
	f.Add("a,b\n1,\"2\n3\"\n")
	f.Add("x;y\n\"unterminated;z\n")
	f.Fuzz(func(t *testing.T, s string) {
		checkExtract(t, documents.FormatCSV, []byte(s))
		checkExtract(t, documents.FormatTSV, []byte(s))
	})
}

func FuzzPDF(f *testing.F) {
	f.Add(minimalPDF("hello"))
	f.Add(minimalPDF(""))
	f.Fuzz(func(t *testing.T, data []byte) { checkExtract(t, documents.FormatPDF, data) })
}
