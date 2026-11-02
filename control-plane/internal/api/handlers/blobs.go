package handlers

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
)

// Blob endpoints: upload a file once, then name it in messages by blob_ref.
//
//	POST   /api/v1/blobs        multipart (field "file", optional "name") or a raw body
//	GET    /api/v1/blobs/{ref}  the bytes, for the kitchen that owns them
//	DELETE /api/v1/blobs/{ref}  drops the kitchen's ownership
//
// A ref belongs to the kitchens that uploaded the bytes. Any other kitchen is told 404, the same
// answer as for a ref that never existed, so a ref is not a credential and its existence cannot
// be probed. The size limit is AGENTOVEN_BLOB_MAX_BYTES (default 25 MB).

// blobMimeAllowed is what the harness can hand a model: images, audio, video and PDF. Anything
// else is not media and is refused rather than stored for no purpose.
func blobMimeAllowed(m string) bool {
	return strings.HasPrefix(m, "image/") || strings.HasPrefix(m, "audio/") || strings.HasPrefix(m, "video/") || m == "application/pdf"
}

func (h *Handlers) blobStore(w http.ResponseWriter) blobstore.Store {
	if h.Router == nil || h.Router.Blobs() == nil {
		respondError(w, http.StatusServiceUnavailable, "the blob store is not available on this server")
		return nil
	}
	return h.Router.Blobs()
}

// UploadBlob handles POST /api/v1/blobs.
func (h *Handlers) UploadBlob(w http.ResponseWriter, r *http.Request) {
	store := h.blobStore(w)
	if store == nil {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	limit := blobstore.MaxBytesFromEnv()

	var data []byte
	var mimeType, name string
	var err error

	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		// Allow for the multipart framing around the file itself.
		r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
		mr, merr := r.MultipartReader()
		if merr != nil {
			respondError(w, http.StatusBadRequest, "invalid multipart body: "+merr.Error())
			return
		}
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				h.blobReadError(w, perr)
				return
			}
			switch part.FormName() {
			case "name":
				b, _ := io.ReadAll(io.LimitReader(part, 512))
				name = strings.TrimSpace(string(b))
			case "file":
				if data != nil {
					respondError(w, http.StatusBadRequest, "send one file per upload")
					return
				}
				if name == "" {
					name = filepath.Base(part.FileName())
				}
				mimeType, _, _ = mime.ParseMediaType(part.Header.Get("Content-Type"))
				data, err = io.ReadAll(io.LimitReader(part, limit+1))
				if err != nil {
					h.blobReadError(w, err)
					return
				}
			}
		}
		if data == nil {
			respondError(w, http.StatusBadRequest, `multipart upload needs a "file" field`)
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, limit+1)
		data, err = io.ReadAll(r.Body)
		if err != nil {
			h.blobReadError(w, err)
			return
		}
		mimeType = ct
		name = firstNonEmpty(r.URL.Query().Get("name"), r.Header.Get("X-Filename"))
	}

	if int64(len(data)) > limit {
		respondError(w, http.StatusRequestEntityTooLarge, "the file is larger than the upload limit of "+strconv.FormatInt(limit, 10)+" bytes")
		return
	}
	if len(data) == 0 {
		respondError(w, http.StatusBadRequest, "the file is empty")
		return
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType, _, _ = mime.ParseMediaType(http.DetectContentType(data))
	}
	if !blobMimeAllowed(mimeType) {
		respondError(w, http.StatusUnsupportedMediaType, "only images, audio, video and PDF can be uploaded (got "+mimeType+")")
		return
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" {
		name = ""
	}

	ref, err := store.Put(r.Context(), kitchen, mimeType, name, data)
	switch {
	case errors.Is(err, blobstore.ErrTooLarge):
		respondError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	case err != nil:
		respondError(w, http.StatusInternalServerError, "could not store the file")
		return
	}
	respondJSON(w, http.StatusCreated, map[string]interface{}{
		"blob_ref": ref, "mime_type": mimeType, "name": name, "size": len(data),
	})
}

func (h *Handlers) blobReadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		respondError(w, http.StatusRequestEntityTooLarge, "the upload is larger than the limit of "+strconv.FormatInt(blobstore.MaxBytesFromEnv(), 10)+" bytes")
		return
	}
	respondError(w, http.StatusBadRequest, "could not read the upload: "+err.Error())
}

// GetBlob handles GET /api/v1/blobs/{ref}.
func (h *Handlers) GetBlob(w http.ResponseWriter, r *http.Request) {
	store := h.blobStore(w)
	if store == nil {
		return
	}
	blob, err := store.Get(r.Context(), middleware.GetKitchen(r.Context()), chi.URLParam(r, "ref"))
	if err != nil {
		// A malformed ref and another kitchen's ref read the same as a missing one.
		respondError(w, http.StatusNotFound, "blob not found")
		return
	}
	w.Header().Set("Content-Type", blob.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if blob.Name != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": blob.Name}))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob.Data)
}

// DeleteBlob handles DELETE /api/v1/blobs/{ref}.
func (h *Handlers) DeleteBlob(w http.ResponseWriter, r *http.Request) {
	store := h.blobStore(w)
	if store == nil {
		return
	}
	kitchen := middleware.GetKitchen(r.Context())
	ref := chi.URLParam(r, "ref")
	if _, err := blobstore.Stat(r.Context(), store, kitchen, ref); err != nil {
		respondError(w, http.StatusNotFound, "blob not found")
		return
	}
	if err := store.Delete(r.Context(), kitchen, ref); err != nil {
		respondError(w, http.StatusInternalServerError, "could not delete the file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkBlobRefs refuses a message that names a blob the kitchen does not own, before any model is
// called: the answer is the same 404 whether the ref is another kitchen's or does not exist.
func (h *Handlers) checkBlobRefs(r *http.Request, kitchen string, parts []models.ContentPart) (string, bool) {
	for _, p := range parts {
		if p.Media == nil || p.Media.BlobRef == "" {
			continue
		}
		if h.Router == nil || h.Router.Blobs() == nil {
			return "blob_ref was given but the blob store is not available on this server", false
		}
		if _, err := blobstore.Stat(r.Context(), h.Router.Blobs(), kitchen, p.Media.BlobRef); err != nil {
			return "blob not found: " + p.Media.BlobRef, false
		}
	}
	return "", true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
