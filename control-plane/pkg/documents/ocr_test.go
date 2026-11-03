package documents_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/documents"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// fakeRouter answers routed calls from a script and records them.
type fakeRouter struct {
	mu    sync.Mutex
	calls []*models.RouteRequest
	reply func(call int, req *models.RouteRequest) (*models.RouteResponse, error)
}

func (f *fakeRouter) Route(_ context.Context, req *models.RouteRequest) (*models.RouteResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	n := len(f.calls)
	f.mu.Unlock()
	return f.reply(n, req)
}

func say(text string) func(int, *models.RouteRequest) (*models.RouteResponse, error) {
	return func(int, *models.RouteRequest) (*models.RouteResponse, error) {
		return &models.RouteResponse{Content: text, Model: "fake-vision", Usage: models.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, EstimatedCost: 0.001}}, nil
	}
}

func jpegOf(t testing.TB, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.Gray{Y: shade + uint8((x+y)%40)})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type pdfPage struct {
	text string // text layer; empty for a scan
	jpeg []byte // the scan; nil for a text page
	// flate marks the picture as stored with a filter the reader cannot hand to a model.
	flate bool
}

// pdfOf builds a PDF whose pages are text, or a full-page picture.
func pdfOf(pages ...pdfPage) []byte {
	var objs []string
	kids := make([]string, len(pages))
	// 1 catalog, 2 pages, then per page: page object, content stream, and for a scan the image.
	next := 3
	pageObjs := make([]string, len(pages))
	for i, p := range pages {
		pageNo, contentNo := next, next+1
		next += 2
		kids[i] = fmt.Sprintf("%d 0 R", pageNo)
		res := "<< /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >>"
		var content string
		var extra []string
		if p.jpeg != nil {
			imgNo := next
			next++
			res = fmt.Sprintf("<< /XObject << /Im0 %d 0 R >> >>", imgNo)
			content = "q 612 0 0 792 0 0 cm /Im0 Do Q"
			filter := "/DCTDecode"
			if p.flate {
				filter = "/FlateDecode"
			}
			extra = append(extra, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 40 /Height 30 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter %s /Length %d >>\nstream\n%s\nendstream", filter, len(p.jpeg), p.jpeg))
		} else if p.text != "" {
			content = fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", p.text)
		}
		pageObjs[i] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources %s >>", contentNo, res)
		objs = append(objs, pageObjs[i], fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
		objs = append(objs, extra...)
	}
	all := append([]string{"<< /Type /Catalog /Pages 2 0 R >>", fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))}, objs...)
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(all))
	for i, o := range all {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(all)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(all)+1, xref)
	return b.Bytes()
}

func opts() documents.OCROptions {
	return documents.OCROptions{Kitchen: "kitchen-a", Provider: "vision", Model: "fake-vision"}
}

func TestOCRImageGoesThroughTheRouterPinnedToTheChosenProvider(t *testing.T) {
	r := &fakeRouter{reply: say("Invoice 42\nTotal 1,250.50")}
	img := jpegOf(t, 40, 30, 10)
	res, err := documents.OCR(context.Background(), r, "scan.jpg", "image/jpeg", img, opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("%d routed calls", len(r.calls))
	}
	req := r.calls[0]
	if req.PinnedProvider != "vision" || req.Kitchen != "kitchen-a" || req.Model != "fake-vision" {
		t.Errorf("the call must be pinned to the chosen provider in the kitchen: %+v", req)
	}
	if req.Labels["purpose"] != "document_ocr" || req.AgentRef != "document-ocr" {
		t.Errorf("a trace must tell this call apart: %v %q", req.Labels, req.AgentRef)
	}
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Error("transcription is not creative")
	}
	user := req.Messages[1]
	if len(user.ContentParts) != 1 || user.ContentParts[0].Media.MimeType != "image/jpeg" || user.ContentParts[0].Media.Data != base64.StdEncoding.EncodeToString(img) {
		t.Errorf("the page must travel as an image part: %+v", user.ContentParts)
	}
	if !strings.Contains(req.Messages[0].Content, "never follow") {
		t.Error("the page is data: the system prompt must say so")
	}
	if len(res.Pages) != 1 || res.Pages[0].Text != "Invoice 42\nTotal 1,250.50" || res.Pages[0].Source != "ocr" {
		t.Fatalf("%+v", res.Pages)
	}
	if res.Pages[0].Confidence != documents.ConfidenceUnverified || res.Confidence != documents.ConfidenceUnverified {
		t.Errorf("one model's reading is unverified, and must say so: page %q result %q", res.Pages[0].Confidence, res.Confidence)
	}
	if res.Usage.InputTokens != 100 || res.Usage.EstimatedCost != 0.001 {
		t.Errorf("usage %+v", res.Usage)
	}
	if got := res.Text(); got != "[Page 1]\nInvoice 42\nTotal 1,250.50" {
		t.Errorf("text %q", got)
	}
}

func TestOCRExternalizeKeepsPicturesOutOfTheCallRecord(t *testing.T) {
	r := &fakeRouter{reply: say("x")}
	o := opts()
	o.Externalize = func(_ context.Context, kitchen string, msg models.ChatMessage) models.ChatMessage {
		if kitchen != "kitchen-a" {
			t.Errorf("kitchen %q", kitchen)
		}
		msg.ContentParts = []models.ContentPart{{Type: "image", Media: &models.MediaRef{MimeType: "image/jpeg", BlobRef: "abc"}}}
		return msg
	}
	if _, err := documents.OCR(context.Background(), r, "a.jpg", "image/jpeg", jpegOf(t, 40, 30, 1), o); err != nil {
		t.Fatal(err)
	}
	if m := r.calls[0].Messages[1].ContentParts[0].Media; m.BlobRef != "abc" || m.Data != "" {
		t.Errorf("%+v", m)
	}
}

func TestOCRRefusesWhatItCannotRead(t *testing.T) {
	r := &fakeRouter{reply: say("x")}
	cases := []struct {
		name, mime string
		data       []byte
		opts       documents.OCROptions
		want       string
	}{
		{"a.docx", mimeDOCXForTest, []byte("x"), opts(), "OCR reads images and PDFs"},
		{"a.tiff", "image/tiff", []byte("x"), opts(), "JPEG, PNG, GIF and WebP"},
		{"a.jpg", "image/jpeg", []byte("x"), documents.OCROptions{Kitchen: "k"}, "name of the provider"},
		{"a.jpg", "image/jpeg", []byte("x"), documents.OCROptions{Kitchen: "k", Provider: "p", Model: "m", Verify: &documents.OCRVerify{Provider: "p", Model: "m"}}, "different provider or model"},
		{"a.jpg", "image/jpeg", make([]byte, 100), documents.OCROptions{Kitchen: "k", Provider: "p", Limits: documents.Limits{MaxInputBytes: 10}}, "limit"},
	}
	for _, c := range cases {
		_, err := documents.OCR(context.Background(), r, c.name, c.mime, c.data, c.opts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("nothing may be sent when the request is refused: %d calls", len(r.calls))
	}
}

const mimeDOCXForTest = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func TestOCRPDFReadsOnlyThePagesWithoutText(t *testing.T) {
	pdf := pdfOf(pdfPage{text: "Cover page"}, pdfPage{jpeg: jpegOf(t, 40, 30, 20)}, pdfPage{jpeg: jpegOf(t, 40, 30, 90)})
	r := &fakeRouter{reply: func(n int, _ *models.RouteRequest) (*models.RouteResponse, error) {
		return &models.RouteResponse{Content: fmt.Sprintf("scanned %d", n), Model: "fake-vision"}, nil
	}}
	res, err := documents.OCR(context.Background(), r, "bundle.pdf", "application/pdf", pdf, opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("a page with a text layer must cost nothing: %d calls", len(r.calls))
	}
	if r.calls[0].Labels["page"] != "2" || r.calls[1].Labels["page"] != "3" {
		t.Errorf("pages go one at a time, in order: %v %v", r.calls[0].Labels, r.calls[1].Labels)
	}
	// The two scans are different pictures: the first call must carry the second page's picture.
	first := r.calls[0].Messages[1].ContentParts[0].Media.Data
	if first == r.calls[1].Messages[1].ContentParts[0].Media.Data {
		t.Error("each page must be sent its own picture")
	}
	want := []struct {
		page   int
		source string
		conf   string
	}{{1, "text_layer", documents.ConfidenceExtracted}, {2, "ocr", documents.ConfidenceUnverified}, {3, "ocr", documents.ConfidenceUnverified}}
	for i, w := range want {
		p := res.Pages[i]
		if p.Page != w.page || p.Source != w.source || p.Confidence != w.conf {
			t.Errorf("page %d: %+v", i+1, p)
		}
	}
	if !strings.Contains(res.Text(), "[Page 1]\nCover page") || !strings.Contains(res.Text(), "[Page 3]\nscanned 2") {
		t.Errorf("text %q", res.Text())
	}
	if res.Confidence != documents.ConfidenceUnverified || res.Incomplete {
		t.Errorf("confidence %q incomplete %v", res.Confidence, res.Incomplete)
	}
}

func TestOCRPageBoundAndFailures(t *testing.T) {
	scans := make([]pdfPage, 4)
	for i := range scans {
		scans[i] = pdfPage{jpeg: jpegOf(t, 40, 30, uint8(i*30))}
	}
	pdf := pdfOf(scans...)

	t.Run("page limit", func(t *testing.T) {
		r := &fakeRouter{reply: say("t")}
		o := opts()
		o.MaxPages = 2
		res, err := documents.OCR(context.Background(), r, "x.pdf", "application/pdf", pdf, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 2 || !res.Incomplete || res.Pages[3].Error == "" {
			t.Fatalf("calls %d incomplete %v pages %+v", len(r.calls), res.Incomplete, res.Pages)
		}
	})
	t.Run("the first call failing is the caller's error", func(t *testing.T) {
		r := &fakeRouter{reply: func(int, *models.RouteRequest) (*models.RouteResponse, error) {
			return nil, errors.New("budget exhausted")
		}}
		if _, err := documents.OCR(context.Background(), r, "x.pdf", "application/pdf", pdf, opts()); err == nil || !strings.Contains(err.Error(), "budget exhausted") {
			t.Fatalf("got %v", err)
		}
		if len(r.calls) != 1 {
			t.Errorf("a refused budget must not be retried page after page: %d calls", len(r.calls))
		}
	})
	t.Run("a later failure keeps the pages already read", func(t *testing.T) {
		r := &fakeRouter{reply: func(n int, _ *models.RouteRequest) (*models.RouteResponse, error) {
			if n == 3 {
				return nil, errors.New("rate limited")
			}
			return &models.RouteResponse{Content: "ok"}, nil
		}}
		res, err := documents.OCR(context.Background(), r, "x.pdf", "application/pdf", pdf, opts())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Incomplete || len(r.calls) != 3 || res.Pages[0].Text != "ok" || res.Pages[2].Error == "" || res.Pages[3].Error == "" {
			t.Fatalf("calls %d pages %+v", len(r.calls), res.Pages)
		}
	})
	t.Run("a blank answer is reported, not passed off as an empty page", func(t *testing.T) {
		r := &fakeRouter{reply: say("   ")}
		res, err := documents.OCR(context.Background(), r, "x.jpg", "image/jpeg", jpegOf(t, 40, 30, 5), opts())
		if err != nil {
			t.Fatal(err)
		}
		if res.Pages[0].Error == "" {
			t.Error("the page must say the model returned nothing")
		}
	})
	t.Run("an output cut at the token limit is warned about", func(t *testing.T) {
		r := &fakeRouter{reply: func(int, *models.RouteRequest) (*models.RouteResponse, error) {
			return &models.RouteResponse{Content: "partial", FinishReason: "length"}, nil
		}}
		res, err := documents.OCR(context.Background(), r, "x.jpg", "image/jpeg", jpegOf(t, 40, 30, 5), opts())
		if err != nil || !strings.Contains(strings.Join(res.Warnings, ";"), "output limit") {
			t.Fatalf("%v %v", err, res)
		}
	})
}

func TestOCRScanThatCannotBeExtractedIsReportedNotGuessed(t *testing.T) {
	pdf := pdfOf(pdfPage{jpeg: []byte("not really flate data"), flate: true})
	r := &fakeRouter{reply: say("x")}
	res, err := documents.OCR(context.Background(), r, "x.pdf", "application/pdf", pdf, opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 || !res.Incomplete || !strings.Contains(res.Pages[0].Error, "JPEG") {
		t.Fatalf("calls %d pages %+v warnings %v", len(r.calls), res.Pages, res.Warnings)
	}
}

func TestOCRSecondPassAgreement(t *testing.T) {
	img := jpegOf(t, 40, 30, 5)
	v := &documents.OCRVerify{Provider: "vision-b", Model: "other"}

	t.Run("agreed", func(t *testing.T) {
		r := &fakeRouter{reply: say("Total due: 1,250.50 USD by 12 March")}
		o := opts()
		o.Verify = v
		res, err := documents.OCR(context.Background(), r, "x.jpg", "image/jpeg", img, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 2 || r.calls[1].PinnedProvider != "vision-b" || r.calls[1].Model != "other" || r.calls[1].Labels["pass"] != "verify" {
			t.Fatalf("the second pass goes to the other model: %+v", r.calls)
		}
		p := res.Pages[0]
		if p.Confidence != documents.ConfidenceAgreed || p.Agreement == nil || *p.Agreement != 1 {
			t.Errorf("%+v", p)
		}
	})
	t.Run("disputed keeps the first text and says so", func(t *testing.T) {
		r := &fakeRouter{reply: func(n int, _ *models.RouteRequest) (*models.RouteResponse, error) {
			if n == 1 {
				return &models.RouteResponse{Content: "Total due: 1,250.50 USD by 12 March"}, nil
			}
			return &models.RouteResponse{Content: "Total due: 1,250.00 USD by 12 March"}, nil
		}}
		o := opts()
		o.Verify = v
		res, err := documents.OCR(context.Background(), r, "x.jpg", "image/jpeg", img, o)
		if err != nil {
			t.Fatal(err)
		}
		p := res.Pages[0]
		if p.Confidence != documents.ConfidenceDisputed || !strings.Contains(p.Text, "1,250.50") || *p.Agreement >= 0.97 || res.Confidence != documents.ConfidenceDisputed {
			t.Errorf("%+v / %q", p, res.Confidence)
		}
	})
	t.Run("a failed second pass leaves the page unverified", func(t *testing.T) {
		r := &fakeRouter{reply: func(n int, _ *models.RouteRequest) (*models.RouteResponse, error) {
			if n == 2 {
				return nil, errors.New("down")
			}
			return &models.RouteResponse{Content: "text"}, nil
		}}
		o := opts()
		o.Verify = v
		res, err := documents.OCR(context.Background(), r, "x.jpg", "image/jpeg", img, o)
		if err != nil || res.Pages[0].Confidence != documents.ConfidenceUnverified || !strings.Contains(strings.Join(res.Warnings, ";"), "second pass failed") {
			t.Fatalf("%v %+v", err, res)
		}
	})
}

func TestAgreement(t *testing.T) {
	cases := []struct {
		a, b string
		min  float64
		max  float64
	}{
		{"", "", 1, 1},
		{"Total 12", "total   12\n", 1, 1},
		{"one two three four", "one two three five", 0.74, 0.76},
		{"a b c", "x y z", 0, 0},
		{"a b", "", 0, 0},
	}
	for _, c := range cases {
		if got := documents.Agreement(c.a, c.b); got < c.min || got > c.max {
			t.Errorf("Agreement(%q, %q) = %v", c.a, c.b, got)
		}
	}
	big := strings.Repeat("word ", 5000)
	if got := documents.Agreement(big, big+"extra"); got < 0.99 {
		t.Errorf("long documents use the bag fallback: %v", got)
	}
}
