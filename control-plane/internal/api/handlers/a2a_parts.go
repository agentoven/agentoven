package handlers

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// An A2A message is a list of parts, and a part is text, a file or structured data. The guardrails used to
// read the text parts only, so a prompt injection carried in a data part, a file's name or a text file's
// bytes reached the agent unchecked. These helpers collect the rest of what a part says, as text a
// guardrail can judge. A file whose bytes are not text (an image, a PDF) is judged by its name and
// location only: its content is not decoded here.

const (
	// maxA2ABody bounds one A2A request or response read into memory. Inline file bytes are base64, so
	// this is a little under 24 MiB of file.
	maxA2ABody = 32 << 20
	// maxA2AFileText bounds the decoded text of one file part that is read for the guardrails.
	maxA2AFileText = 1 << 20
	// maxA2ADepth bounds how deep a data part is walked.
	maxA2ADepth = 32
)

// limitA2ABody makes a read of the request body past maxA2ABody fail instead of exhausting memory.
func limitA2ABody(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxA2ABody)
	}
}

// readA2ABody reads the whole (limited) body. When it cannot, it has already answered the caller and
// ok is false: a body that was cut short must never be judged as if it were whole.
func readA2ABody(w http.ResponseWriter, r *http.Request) (body []byte, ok bool) {
	limitA2ABody(w, r)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": nil,
			"error": map[string]interface{}{"code": -32600, "message": "The request is larger than the gateway accepts"}})
		return nil, false
	}
	return body, true
}

// a2aNonTextRequest is the text of the parts of a request's message that are not text parts.
func a2aNonTextRequest(body []byte) string {
	var doc map[string]interface{}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	params, _ := doc["params"].(map[string]interface{})
	msg, _ := params["message"].(map[string]interface{})
	return nonTextParts(msg["parts"])
}

// a2aNonTextResponse is the same for the parts a result carries: artifacts, the status message, a direct message.
func a2aNonTextResponse(body []byte) string {
	var doc map[string]interface{}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	res, _ := doc["result"].(map[string]interface{})
	var out []string
	if arts, ok := res["artifacts"].([]interface{}); ok {
		for _, a := range arts {
			if am, ok := a.(map[string]interface{}); ok {
				out = append(out, nonTextParts(am["parts"]))
			}
		}
	}
	if status, ok := res["status"].(map[string]interface{}); ok {
		if msg, ok := status["message"].(map[string]interface{}); ok {
			out = append(out, nonTextParts(msg["parts"]))
		}
	}
	out = append(out, nonTextParts(res["parts"]))
	return joinNonEmpty(out)
}

func joinNonEmpty(parts []string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

// nonTextParts renders the parts that are not text parts: a file by its name, location and, when its bytes
// are text, those; structured data by its string values; a part of a kind the gateway does not know by its
// whole JSON, because a proxied agent may still act on it.
func nonTextParts(v interface{}) string {
	list, _ := v.([]interface{})
	var out []string
	for _, p := range list {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := pm["kind"].(string)
		if kind == "" {
			kind, _ = pm["type"].(string)
		}
		switch kind {
		case "text":
			continue
		case "file":
			out = append(out, fileText(pm["file"]))
		case "data":
			var vals []string
			collectStrings(pm["data"], 0, &vals)
			out = append(out, strings.Join(vals, "\n"))
		default:
			if _, isText := pm["text"].(string); kind == "" && isText {
				continue // a part with no kind and a text field is a text part
			}
			raw, _ := json.Marshal(pm)
			out = append(out, string(raw))
		}
	}
	return joinNonEmpty(out)
}

// fileText is what a file part says to a guardrail: its name, its uri and, for a text file, its content.
func fileText(v interface{}) string {
	f, _ := v.(map[string]interface{})
	var out []string
	for _, k := range []string{"name", "uri"} {
		if s, _ := f[k].(string); s != "" {
			out = append(out, s)
		}
	}
	mime, _ := f["mimeType"].(string)
	if b, _ := f["bytes"].(string); b != "" && isTextMime(mime) {
		// Decode at most maxA2AFileText bytes: a longer file is judged by its beginning, never skipped.
		n := base64.StdEncoding.DecodedLen(len(b))
		if n > maxA2AFileText {
			b = b[:maxA2AFileText/3*4]
		}
		if raw, err := base64.StdEncoding.DecodeString(b); err == nil {
			out = append(out, string(raw))
		} else if raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b, "=")); err == nil {
			out = append(out, string(raw))
		}
	}
	return strings.Join(out, "\n")
}

func isTextMime(m string) bool {
	m = strings.ToLower(strings.TrimSpace(strings.SplitN(m, ";", 2)[0]))
	return strings.HasPrefix(m, "text/") || m == "application/json" || m == "application/xml" ||
		m == "application/yaml" || m == "application/x-yaml" || strings.HasSuffix(m, "+json") || strings.HasSuffix(m, "+xml")
}

// collectStrings gathers every string value of a JSON document (object keys are not values).
func collectStrings(v interface{}, depth int, out *[]string) {
	if depth > maxA2ADepth {
		return
	}
	switch t := v.(type) {
	case string:
		if t != "" {
			*out = append(*out, t)
		}
	case []interface{}:
		for _, e := range t {
			collectStrings(e, depth+1, out)
		}
	case map[string]interface{}:
		for _, e := range t {
			collectStrings(e, depth+1, out)
		}
	}
}
