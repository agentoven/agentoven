package handlers_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const (
	docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	xlsxMime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// docEnv is a handler set whose one provider is a fake OpenAI-compatible endpoint answering from
// a script, so the document routes can be driven end to end through the real router.
type docEnv struct {
	h      *handlers.Handlers
	blobs  *blobstore.LocalStore
	mu     sync.Mutex
	bodies []string
	reply  func(call int, body string) string
	status int // when set, the provider answers with this status and an error
}

func newDocEnv(t *testing.T) *docEnv {
	t.Helper()
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	e := &docEnv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.bodies = append(e.bodies, string(raw))
		n := len(e.bodies)
		e.mu.Unlock()
		if e.status != 0 {
			http.Error(w, `{"error":{"message":"refused"}}`, e.status)
			return
		}
		content := "ok"
		if e.reply != nil {
			content = e.reply(n, string(raw))
		}
		out, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 30, "completion_tokens": 10, "total_tokens": 40},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)

	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	bs, err := blobstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mr.SetBlobStore(bs)
	for _, kitchen := range []string{"default", "team-a", "team-b"} {
		if err := s.CreateProvider(context.Background(), &models.ModelProvider{
			Name: "vision", Kitchen: kitchen, Kind: "openai", Endpoint: srv.URL, Models: []string{"gpt-4o"}, IsDefault: true,
			Config: map[string]interface{}{"api_key": "k"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	e.h = handlers.New(s, mr, nil, nil, nil, sessions.NewMemorySessionStore(), nil)
	e.blobs = bs
	return e
}

func (e *docEnv) upstream() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.bodies, "\n")
}

func (e *docEnv) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.bodies)
}

func (e *docEnv) put(t *testing.T, kitchen, mimeType, name string, data []byte) string {
	t.Helper()
	ref, err := e.blobs.Put(context.Background(), kitchen, mimeType, name, data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func (e *docEnv) postJSON(h http.HandlerFunc, kitchen string, body interface{}) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/x", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, withKitchen(req, kitchen))
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

func officeZip(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, _ := zw.Create(f[0])
		_, _ = w.Write([]byte(f[1]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func docxOf(t *testing.T, text string) []byte {
	return officeZip(t,
		[2]string{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		[2]string{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="x/officeDocument" Target="word/document.xml"/></Relationships>`},
		[2]string{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`},
	)
}

func TestExtractReadsABlobTheKitchenOwns(t *testing.T) {
	e := newDocEnv(t)
	ref := e.put(t, "team-a", docxMime, "claim.docx", docxOf(t, "Garage invoice 420.00"))

	rec := e.postJSON(e.h.ExtractDocument, "team-a", map[string]string{"blob_ref": ref})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	if out["format"] != "docx" || !strings.Contains(out["text"].(string), "Garage invoice 420.00") || out["name"] != "claim.docx" || out["blob_ref"] != ref {
		t.Fatalf("%v", out)
	}
	if blocks, _ := out["blocks"].([]interface{}); len(blocks) != 1 {
		t.Errorf("the structure must come back too: %v", out["blocks"])
	}
	if e.calls() != 0 {
		t.Error("extract never calls a model")
	}

	for _, kitchen := range []string{"team-b", "default"} {
		if rec := e.postJSON(e.h.ExtractDocument, kitchen, map[string]string{"blob_ref": ref}); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Garage") {
			t.Errorf("%s: another kitchen's ref is a plain 404, got %d %s", kitchen, rec.Code, rec.Body.String())
		}
	}
	if rec := e.postJSON(e.h.ExtractDocument, "team-a", map[string]string{"blob_ref": "../../etc/passwd"}); rec.Code != http.StatusNotFound {
		t.Errorf("a malformed ref reads as a missing one: %d", rec.Code)
	}
	if rec := e.postJSON(e.h.ExtractDocument, "team-a", map[string]string{}); rec.Code != http.StatusBadRequest {
		t.Errorf("no ref and no file: %d", rec.Code)
	}
}

func TestExtractReadsAnUploadWithoutStoringIt(t *testing.T) {
	e := newDocEnv(t)
	sheet := officeZip(t,
		[2]string{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		[2]string{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="x/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		[2]string{"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Lines" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		[2]string{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		[2]string{"xl/worksheets/sheet1.xml", `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="3"><c r="B3" t="inlineStr"><is><t>bolt</t></is></c></row></sheetData></worksheet>`},
	)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/extract?name=lines.xlsx", bytes.NewReader(sheet))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	e.h.ExtractDocument(rec, withKitchen(req, "team-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	if out["format"] != "xlsx" || out["mime_type"] != xlsxMime || !strings.Contains(out["text"].(string), "| 3 | bolt |") {
		t.Fatalf("a file named .xlsx sent as octet-stream is read as a workbook: %v", out)
	}
	if _, has := out["blob_ref"]; has {
		t.Error("an upload read for extraction is not stored")
	}
}

func TestExtractRefusalsSayWhy(t *testing.T) {
	e := newDocEnv(t)
	post := func(ctype, name string, data []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/extract?name="+name, bytes.NewReader(data))
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		e.h.ExtractDocument(rec, withKitchen(req, "team-a"))
		return rec
	}
	if rec := post("application/x-msdownload", "x.exe", []byte("MZ")); rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), "cannot be read as text") {
		t.Errorf("unsupported type: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(docxMime, "broken.docx", []byte("PK this is not a zip")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a valid Office file") {
		t.Errorf("a damaged file is 422: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("application/msword", "old.doc", []byte("x")); rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), "legacy") {
		t.Errorf("legacy format: %d %s", rec.Code, rec.Body.String())
	}
	bomb := officeZip(t,
		[2]string{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		[2]string{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="x/officeDocument" Target="word/document.xml"/></Relationships>`},
		[2]string{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><!--` + strings.Repeat("A", 20<<20) + `--></w:document>`},
	)
	if rec := post(docxMime, "bomb.docx", bomb); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a zip bomb is 413: %d %s", rec.Code, rec.Body.String())
	}
	t.Setenv("AGENTOVEN_BLOB_MAX_BYTES", "100")
	if rec := post("text/plain", "big.txt", bytes.Repeat([]byte("a"), 500)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized upload is 413: %d", rec.Code)
	}
}

func TestExtractReportsAScanAsNeedingOCR(t *testing.T) {
	e := newDocEnv(t)
	pdf := blankPDF()
	ref := e.put(t, "team-a", "application/pdf", "scan.pdf", pdf)
	rec := e.postJSON(e.h.ExtractDocument, "team-a", map[string]string{"blob_ref": ref})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if out := decodeBody(t, rec); out["needs_ocr"] != true {
		t.Fatalf("%v", out)
	}
}

func TestOCRThroughTheRouterForTheKitchensOwnProvider(t *testing.T) {
	e := newDocEnv(t)
	e.reply = func(int, string) string { return "Invoice 7\nTotal 99.00" }
	ref := e.put(t, "team-a", "image/png", "scan.png", pngBytes())

	rec := e.postJSON(e.h.OCRDocument, "team-a", map[string]interface{}{"blob_ref": ref, "provider": "vision", "model": "gpt-4o"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	if out["text"] != "[Page 1]\nInvoice 7\nTotal 99.00" || out["confidence"] != "unverified" || out["provider"] != "vision" {
		t.Fatalf("%v", out)
	}
	if !strings.Contains(out["confidence_note"].(string), "not a probability") {
		t.Error("the response must say what the confidence means")
	}
	if usage := out["usage"].(map[string]interface{}); usage["total_tokens"].(float64) != 40 {
		t.Errorf("the call's cost is reported: %v", usage)
	}
	if !strings.Contains(e.upstream(), "data:image/png;base64,") {
		t.Error("the page must reach the provider as an image")
	}

	cases := []struct {
		name    string
		kitchen string
		body    map[string]interface{}
		want    int
	}{
		{"another kitchen's blob", "team-b", map[string]interface{}{"blob_ref": ref, "provider": "vision"}, http.StatusNotFound},
		{"no provider", "team-a", map[string]interface{}{"blob_ref": ref}, http.StatusBadRequest},
		{"unknown provider", "team-a", map[string]interface{}{"blob_ref": ref, "provider": "nope"}, http.StatusNotFound},
		{"no blob", "team-a", map[string]interface{}{"provider": "vision"}, http.StatusBadRequest},
		{"second pass on the same model", "team-a", map[string]interface{}{"blob_ref": ref, "provider": "vision", "model": "gpt-4o", "verify": map[string]string{"provider": "vision", "model": "gpt-4o"}}, http.StatusUnprocessableEntity},
	}
	before := e.calls()
	for _, c := range cases {
		if rec := e.postJSON(e.h.OCRDocument, c.kitchen, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if e.calls() != before {
		t.Error("a refused request sends nothing to the provider")
	}
	docx := e.put(t, "team-a", docxMime, "a.docx", docxOf(t, "x"))
	if rec := e.postJSON(e.h.OCRDocument, "team-a", map[string]interface{}{"blob_ref": docx, "provider": "vision"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("OCR reads images and PDFs: %d", rec.Code)
	}
}

func TestOCRBlankAnswerIsReportedOnThePage(t *testing.T) {
	e := newDocEnv(t)
	ref := e.put(t, "team-a", "image/png", "scan.png", pngBytes())
	e.reply = func(int, string) string { return "" }
	rec := e.postJSON(e.h.OCRDocument, "team-a", map[string]interface{}{"blob_ref": ref, "provider": "vision"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "returned no text") {
		t.Errorf("a blank answer is reported on the page: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOCRProviderFailureIsA502(t *testing.T) {
	e := newDocEnv(t)
	ref := e.put(t, "team-a", "image/png", "scan.png", pngBytes())
	e.status = http.StatusBadRequest
	rec := e.postJSON(e.h.OCRDocument, "team-a", map[string]interface{}{"blob_ref": ref, "provider": "vision"})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("a provider that refuses the call is a 502: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFieldsFromTextAndFromAScan(t *testing.T) {
	e := newDocEnv(t)
	schema := map[string]interface{}{"type": "object", "properties": map[string]interface{}{
		"invoice_number": map[string]interface{}{"type": "string"},
		"total":          map[string]interface{}{"type": "number"},
	}, "required": []string{"invoice_number"}}
	e.reply = func(int, string) string {
		return `{"fields":{"invoice_number":{"value":"INV-7","page":0,"quote":"Invoice No. INV-7"},"total":{"value":100,"page":0,"quote":"Total 99.00"}}}`
	}
	rec := e.postJSON(e.h.ExtractDocumentFields, "team-a", map[string]interface{}{
		"text": "ACME\nInvoice No. INV-7\nTotal 99.00", "schema": schema, "provider": "vision", "model": "gpt-4o",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	fields := out["fields"].([]interface{})
	inv, total := fields[0].(map[string]interface{}), fields[1].(map[string]interface{})
	if inv["name"] != "invoice_number" || inv["status"] != "grounded" {
		t.Errorf("%v", inv)
	}
	if total["name"] != "total" || total["status"] != "ungrounded" || total["flags"].([]interface{})[0] != "value_not_in_quote" {
		t.Errorf("the model said 100 but the quote says 99.00: %v", total)
	}
	if out["all_grounded"] != false {
		t.Errorf("%v", out["all_grounded"])
	}

	// From a blob: a docx is read in-process, and the quotes are checked against its text.
	ref := e.put(t, "team-a", docxMime, "inv.docx", docxOf(t, "Invoice No. INV-7 Total 99.00"))
	rec = e.postJSON(e.h.ExtractDocumentFields, "team-a", map[string]interface{}{"blob_ref": ref, "schema": schema, "provider": "vision"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"grounded"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	// A scan without an ocr object says what to do; with one, the pages are transcribed first.
	img := e.put(t, "team-a", "image/png", "scan.png", pngBytes())
	if rec := e.postJSON(e.h.ExtractDocumentFields, "team-a", map[string]interface{}{"blob_ref": img, "schema": schema, "provider": "vision"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `ocr\" object`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	e.reply = func(_ int, body string) string {
		if strings.Contains(body, "image_url") {
			return "Invoice No. INV-7\nTotal 99.00"
		}
		return `{"fields":{"invoice_number":{"value":"INV-7","page":1,"quote":"Invoice No. INV-7"},"total":{"value":99,"page":1,"quote":"Total 99.00"}}}`
	}
	rec = e.postJSON(e.h.ExtractDocumentFields, "team-a", map[string]interface{}{"blob_ref": img, "schema": schema, "provider": "vision", "ocr": map[string]interface{}{}})
	out = decodeBody(t, rec)
	if rec.Code != http.StatusOK || out["all_grounded"] != true {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.Join(toStrings(out["warnings"]), ";"), "confidence unverified") {
		t.Errorf("fields read from a transcription must carry how the transcription was checked: %v", out["warnings"])
	}

	if rec := e.postJSON(e.h.ExtractDocumentFields, "team-b", map[string]interface{}{"blob_ref": ref, "schema": schema, "provider": "vision"}); rec.Code != http.StatusNotFound {
		t.Errorf("another kitchen's blob: %d", rec.Code)
	}
	for name, body := range map[string]map[string]interface{}{
		"both blob and text": {"blob_ref": ref, "text": "x", "schema": schema, "provider": "vision"},
		"neither":            {"schema": schema, "provider": "vision"},
		"no provider":        {"text": "x", "schema": schema},
	} {
		if rec := e.postJSON(e.h.ExtractDocumentFields, "team-a", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := e.postJSON(e.h.ExtractDocumentFields, "team-a", map[string]interface{}{"text": "x", "provider": "vision", "schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"a": map[string]interface{}{"type": "object"}}}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a schema that is not supported is 422: %d", rec.Code)
	}
}

func toStrings(v interface{}) []string {
	var out []string
	for _, s := range v.([]interface{}) {
		out = append(out, s.(string))
	}
	return out
}

func TestUploadAcceptsDocumentsAndSettlesTheirType(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	for _, c := range []struct{ ctype, name, want string }{
		{"application/zip", "claim.docx", docxMime},
		{"application/octet-stream", "lines.xlsx", xlsxMime},
		{"text/plain", "data.csv", "text/csv"},
		{"text/html", "page.html", "text/html"},
	} {
		rec := e.uploadRaw("default", c.ctype, "?name="+c.name, []byte("some bytes of "+c.name))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if got := decodeBody(t, rec)["mime_type"]; got != c.want {
			t.Errorf("%s: stored as %v, want %s", c.name, got, c.want)
		}
	}
}

// blankPDF is a one-page PDF with no text: what a scan without a picture looks like to the reader.
func blankPDF() []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, o))
	}
	xref := b.Len()
	b.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objs)+1))
	for _, off := range offsets {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	b.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref))
	return b.Bytes()
}
