package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// Gemini takes media inline (base64 in the request, 20 MB for the whole request) or by file URI
// from its Files API. A large video or PDF should go up once and be referenced after that: the
// request stays small and a repeated file is not sent again. The URI is recorded in the blob
// store like the OpenAI and Anthropic file ids. Gemini deletes uploaded files after 48 hours, so
// the record carries the expiry and an expired URI is uploaded again.

// EnvGeminiFilesMinBytes is the size from which a blob-backed part goes through the Files API
// instead of inline. 0 sends everything inline.
const EnvGeminiFilesMinBytes = "AGENTOVEN_GEMINI_FILES_MIN_BYTES"

const defaultGeminiFilesMinBytes = 4 << 20

// geminiFileTTL is how long a recorded URI is trusted: Gemini keeps files 48 hours, and a margin
// covers a request already in flight when the file expires.
const geminiFileTTL = 46 * time.Hour

// geminiProcessingPoll is how often, and geminiProcessingMax how long, to wait for an uploaded
// video to finish processing. Variables so tests do not wait.
var (
	geminiProcessingPoll = time.Second
	geminiProcessingMax  = 90 * time.Second
)

func geminiFilesMinBytes() int64 {
	if v := os.Getenv(EnvGeminiFilesMinBytes); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return defaultGeminiFilesMinBytes
}

// geminiFileURI returns the Files API URI for a blob-backed part that is large enough, uploading
// it the first time. "" means send the part inline: not blob-backed, small, the Files API
// refused it, or no API key.
func (mr *ModelRouter) geminiFileURI(ctx context.Context, prov *models.ModelProvider, ref *models.MediaRef) string {
	min := geminiFilesMinBytes()
	if min == 0 || ref == nil || ref.BlobRef == "" || mr.blobs == nil {
		return ""
	}
	key := fileKey(prov)
	if rec, ok, err := mr.blobs.ProviderFileID(ctx, ref.BlobRef, key); err == nil && ok {
		if uri, valid := parseGeminiRecord(rec, time.Now()); valid {
			return uri
		}
	}
	blob, err := mr.blobs.Get(ctx, blobKitchen(ctx), ref.BlobRef)
	if err != nil || int64(len(blob.Data)) < min {
		return ""
	}
	apiKey, _ := prov.Config["api_key"].(string)
	if apiKey == "" {
		return ""
	}
	uri, expires, err := uploadGeminiFile(ctx, mr.clientFor(prov), geminiEndpoint(prov), apiKey,
		nonEmpty(ref.Name, "media"), nonEmpty(ref.MimeType, blob.MimeType), blob.Data)
	if err != nil {
		log.Warn().Err(err).Str("provider", prov.Name).Msg("Could not upload media to Gemini's Files API; sending it inline instead")
		return ""
	}
	if err := mr.blobs.SetProviderFileID(ctx, ref.BlobRef, key, formatGeminiRecord(uri, expires)); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
		log.Warn().Err(err).Msg("Could not record the Gemini file URI")
	}
	return uri
}

// A recorded Gemini file is "<uri>|<unix expiry>".
func formatGeminiRecord(uri string, expires time.Time) string {
	return uri + "|" + strconv.FormatInt(expires.Unix(), 10)
}

func parseGeminiRecord(rec string, now time.Time) (uri string, valid bool) {
	i := strings.LastIndex(rec, "|")
	if i < 0 {
		return "", false
	}
	exp, err := strconv.ParseInt(rec[i+1:], 10, 64)
	if err != nil || !now.Before(time.Unix(exp, 0)) {
		return "", false
	}
	return rec[:i], true
}

// geminiUploadURL turns the chat base (https://host/v1beta) into the Files upload URL
// (https://host/upload/v1beta/files).
func geminiUploadURL(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return "", err
	}
	u.Path = "/upload" + strings.TrimRight(u.Path, "/") + "/files"
	return u.String(), nil
}

type geminiFileResponse struct {
	File struct {
		Name           string `json:"name"`
		URI            string `json:"uri"`
		State          string `json:"state"`
		ExpirationTime string `json:"expirationTime"`
	} `json:"file"`
}

// uploadGeminiFile sends the bytes with the multipart upload protocol and waits until Gemini has
// processed them (video needs it), returning the file URI and when it expires.
func uploadGeminiFile(ctx context.Context, client *http.Client, endpoint, apiKey, name, mime string, data []byte) (string, time.Time, error) {
	upURL, err := geminiUploadURL(endpoint)
	if err != nil {
		return "", time.Time{}, err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	metaHdr := textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}}
	mp, err := mw.CreatePart(metaHdr)
	if err != nil {
		return "", time.Time{}, err
	}
	meta, _ := json.Marshal(map[string]interface{}{"file": map[string]string{"display_name": name}})
	_, _ = mp.Write(meta)
	dp, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mime}})
	if err != nil {
		return "", time.Time{}, err
	}
	_, _ = dp.Write(data)
	if err := mw.Close(); err != nil {
		return "", time.Time{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upURL, &body)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
	req.Header.Set("X-Goog-Upload-Protocol", "multipart")
	req.Header.Set("x-goog-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("files API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out geminiFileResponse
	if err := json.Unmarshal(raw, &out); err != nil || out.File.URI == "" {
		return "", time.Time{}, fmt.Errorf("files API response had no file uri")
	}

	deadline := time.Now().Add(geminiProcessingMax)
	for strings.EqualFold(out.File.State, "PROCESSING") {
		if time.Now().After(deadline) {
			return "", time.Time{}, fmt.Errorf("file %s is still processing", out.File.Name)
		}
		select {
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		case <-time.After(geminiProcessingPoll):
		}
		getURL := strings.TrimRight(endpoint, "/") + "/" + out.File.Name
		greq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
		if err != nil {
			return "", time.Time{}, err
		}
		greq.Header.Set("x-goog-api-key", apiKey)
		gresp, err := client.Do(greq)
		if err != nil {
			return "", time.Time{}, err
		}
		graw, _ := io.ReadAll(gresp.Body)
		gresp.Body.Close()
		if gresp.StatusCode != http.StatusOK {
			return "", time.Time{}, fmt.Errorf("files API returned %d while waiting for %s", gresp.StatusCode, out.File.Name)
		}
		var next geminiFileResponse
		if err := json.Unmarshal(graw, &next.File); err != nil {
			return "", time.Time{}, err
		}
		out.File.State = next.File.State
		if next.File.URI != "" {
			out.File.URI = next.File.URI
		}
	}
	if strings.EqualFold(out.File.State, "FAILED") {
		return "", time.Time{}, fmt.Errorf("gemini could not process the file")
	}
	expires := time.Now().Add(geminiFileTTL)
	if t, err := time.Parse(time.RFC3339, out.File.ExpirationTime); err == nil && t.Before(expires.Add(2*time.Hour)) {
		expires = t.Add(-time.Hour)
	}
	return out.File.URI, expires, nil
}
