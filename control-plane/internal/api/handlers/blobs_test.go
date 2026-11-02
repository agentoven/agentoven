package handlers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func pngBytes() []byte {
	b, _ := base64.StdEncoding.DecodeString(tinyPNG)
	return b
}

func (e *mmEnv) uploadRaw(kitchen, contentType, query string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blobs"+query, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	e.h.UploadBlob(rec, withKitchen(req, kitchen))
	return rec
}

func (e *mmEnv) uploadMultipart(kitchen, filename, partType string, data []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	if partType != "" {
		hdr.Set("Content-Type", partType)
	}
	pw, _ := mw.CreatePart(hdr)
	_, _ = pw.Write(data)
	_ = mw.Close()
	return e.uploadRaw(kitchen, mw.FormDataContentType(), "", buf.Bytes())
}

func (e *mmEnv) blobCall(h http.HandlerFunc, method, kitchen, ref string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/blobs/"+ref, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("ref", ref)
	req = withKitchen(req, kitchen)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func refOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out["blob_ref"].(string)
}

func TestUploadRawAndMultipartReturnABlobRef(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")

	rec := e.uploadRaw("default", "image/png", "?name=cat.png", pngBytes())
	raw := refOf(t, rec)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["mime_type"] != "image/png" || out["name"] != "cat.png" || int(out["size"].(float64)) != len(pngBytes()) {
		t.Fatalf("unexpected response %v", out)
	}

	// Multipart without a part type: the type is sniffed from the bytes.
	mp := refOf(t, e.uploadMultipart("default", "photo.bin", "", pngBytes()))
	if mp != raw {
		t.Fatal("the same bytes are one blob however they were uploaded")
	}
	rec = e.uploadMultipart("default", "../../etc/evil.pdf", "application/pdf", []byte("%PDF-1.7 x"))
	_ = refOf(t, rec)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["name"] != "evil.pdf" {
		t.Fatalf("a path in the file name must be reduced to its base name, got %v", out["name"])
	}
}

func TestUploadRefusesWhatIsNotMediaAndWhatIsTooBig(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")

	if rec := e.uploadRaw("default", "text/html", "", []byte("<script>alert(1)</script>")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("HTML is not media: %d", rec.Code)
	}
	if rec := e.uploadRaw("default", "application/octet-stream", "", []byte("just some text bytes")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("sniffed text is not media: %d", rec.Code)
	}
	if rec := e.uploadRaw("default", "image/png", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty upload: %d", rec.Code)
	}

	t.Setenv("AGENTOVEN_BLOB_MAX_BYTES", "80")
	big := append(pngBytes(), bytes.Repeat([]byte{0}, 100)...)
	if rec := e.uploadRaw("default", "image/png", "", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("raw over the limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.uploadMultipart("default", "x.png", "image/png", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("multipart over the limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.uploadRaw("default", "image/png", "", pngBytes()); rec.Code != http.StatusCreated {
		t.Fatalf("under the limit is fine: %d", rec.Code)
	}
}

func TestOnlyTheOwningKitchenCanReadOrDeleteABlob(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	ref := refOf(t, e.uploadRaw("team-a", "image/png", "?name=a.png", pngBytes()))

	rec := e.blobCall(e.h.GetBlob, http.MethodGet, "team-a", ref)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pngBytes()) {
		t.Fatalf("owner read: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("served files must not be sniffed or rendered: %v", rec.Header())
	}
	missing := e.blobCall(e.h.GetBlob, http.MethodGet, "team-b", strings.Repeat("0", 64))
	other := e.blobCall(e.h.GetBlob, http.MethodGet, "team-b", ref)
	if other.Code != http.StatusNotFound || other.Body.String() != missing.Body.String() {
		t.Fatalf("another kitchen must get the same 404 as for a missing ref: %d %s vs %s", other.Code, other.Body.String(), missing.Body.String())
	}
	if rec := e.blobCall(e.h.DeleteBlob, http.MethodDelete, "team-b", ref); rec.Code != http.StatusNotFound {
		t.Fatalf("another kitchen cannot delete: %d", rec.Code)
	}
	if rec := e.blobCall(e.h.GetBlob, http.MethodGet, "team-a", "../../etc/passwd"); rec.Code != http.StatusNotFound {
		t.Fatalf("a malformed ref is a 404: %d", rec.Code)
	}
	if rec := e.blobCall(e.h.DeleteBlob, http.MethodDelete, "team-a", ref); rec.Code != http.StatusNoContent {
		t.Fatalf("owner delete: %d", rec.Code)
	}
	if rec := e.blobCall(e.h.GetBlob, http.MethodGet, "team-a", ref); rec.Code != http.StatusNotFound {
		t.Fatal("deleted")
	}
}

// An uploaded file is named by blob_ref in every way a message can carry media.
func TestBlobRefIsAcceptedAsAPartSourceEverywhere(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	ref := refOf(t, e.uploadRaw("default", "image/png", "", pngBytes()))
	refPart := map[string]interface{}{"type": "image", "media": map[string]string{"mime_type": "image/png", "blob_ref": ref}}
	wantImage := "data:image/png;base64," + tinyPNG

	// session
	sid := e.newSession(t)
	if rec := e.sendSession(t, sid, map[string]interface{}{"content": "look", "content_parts": []interface{}{refPart}}); rec.Code != http.StatusOK {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	// invoke
	body, _ := json.Marshal(map[string]interface{}{"message": "look", "content_parts": []interface{}{refPart}})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))), "default"), "agentName", "ag")
	rec := httptest.NewRecorder()
	e.h.InvokeAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invoke: %d %s", rec.Code, rec.Body.String())
	}
	// A2A
	out := a2aSend(t, e, []map[string]interface{}{
		{"type": "text", "text": "look"},
		{"type": "file", "file": map[string]string{"mimeType": "image/png", "blob_ref": ref}},
	})
	if out["error"] != nil {
		t.Fatalf("a2a: %v", out["error"])
	}
	if got := strings.Count(e.prov.all(), wantImage); got != 3 {
		t.Fatalf("each of the three sends must carry the stored image, saw %d", got)
	}
}

func TestAnotherKitchensBlobRefIsA404OnEverySendPath(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	ref := refOf(t, e.uploadRaw("team-a", "image/png", "", pngBytes()))
	refPart := map[string]interface{}{"type": "image", "media": map[string]string{"mime_type": "image/png", "blob_ref": ref}}

	sid := e.newSession(t) // the session and agent belong to "default"
	if rec := e.sendSession(t, sid, map[string]interface{}{"content": "look", "content_parts": []interface{}{refPart}}); rec.Code != http.StatusNotFound {
		t.Fatalf("session: expected 404, got %d %s", rec.Code, rec.Body.String())
	}
	body, _ := json.Marshal(map[string]interface{}{"message": "look", "content_parts": []interface{}{refPart}})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))), "default"), "agentName", "ag")
	rec := httptest.NewRecorder()
	e.h.InvokeAgent(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invoke: expected 404, got %d %s", rec.Code, rec.Body.String())
	}
	out := a2aSend(t, e, []map[string]interface{}{{"type": "file", "file": map[string]string{"mimeType": "image/png", "blob_ref": ref}}})
	if out["error"] == nil {
		t.Fatal("a2a must refuse another kitchen's blob")
	}
	if e.prov.count() != 0 {
		t.Fatal("nothing reaches the model")
	}
}

// voice_output on an A2A task returns the spoken reply as an audio FilePart artifact.
func TestA2ATaskCanReturnTheReplyAsAnAudioFilePart(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	send := func(cfg map[string]interface{}) map[string]interface{} {
		body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tasks/send", "params": map[string]interface{}{
			"message":       map[string]interface{}{"role": "user", "parts": []map[string]interface{}{{"type": "text", "text": "say hi"}}},
			"metadata":      map[string]string{"agent_name": "ag"},
			"configuration": cfg,
		}})
		rec := httptest.NewRecorder()
		e.h.A2AEndpoint(rec, withKitchen(httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(string(body))), "default"))
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	out := send(map[string]interface{}{"voice_output": true, "voice": "nova"})
	res := out["result"].(map[string]interface{})
	parts := res["artifacts"].([]interface{})[0].(map[string]interface{})["parts"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("expected a text part and an audio file part, got %v", parts)
	}
	file := parts[1].(map[string]interface{})
	if file["type"] != "file" || file["kind"] != "file" {
		t.Fatalf("not a file part: %v", file)
	}
	f := file["file"].(map[string]interface{})
	raw, _ := base64.StdEncoding.DecodeString(f["bytes"].(string))
	if string(raw) != "SPOKEN" || f["mimeType"] != "audio/mpeg" || f["name"] != "reply.mp3" {
		t.Fatalf("unexpected audio file %v", f)
	}
	if parts[0].(map[string]interface{})["text"] != "seen" {
		t.Fatalf("the text reply stays first: %v", parts[0])
	}

	// A plain task is unchanged.
	plain := send(nil)["result"].(map[string]interface{})["artifacts"].([]interface{})[0].(map[string]interface{})["parts"].([]interface{})
	if len(plain) != 1 {
		t.Fatalf("no audio unless asked: %v", plain)
	}
	// Asked for speech on a background task: refused up front.
	if out := send(map[string]interface{}{"voice_output": true, "blocking": false}); out["error"] == nil {
		t.Fatal("voice_output needs a blocking task")
	}
}

func TestA2AVoiceOutputOnAnAgentWithoutSpeechIsRefusedBeforeItRuns(t *testing.T) {
	e := newMMEnv(t, "anthropic", "claude-sonnet-4-5")
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tasks/send", "params": map[string]interface{}{
		"message":       map[string]interface{}{"role": "user", "parts": []map[string]interface{}{{"type": "text", "text": "hi"}}},
		"metadata":      map[string]string{"agent_name": "ag"},
		"configuration": map[string]interface{}{"voice_output": true},
	}})
	rec := httptest.NewRecorder()
	e.h.A2AEndpoint(rec, withKitchen(httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(string(body))), "default"))
	if !strings.Contains(rec.Body.String(), "audio modality") || e.prov.count() != 0 {
		t.Fatalf("expected a clear refusal with no model call: %s", rec.Body.String())
	}
}
