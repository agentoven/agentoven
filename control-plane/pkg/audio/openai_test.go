package audio_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/audio"
)

func fakeOpenAI(t *testing.T, check func(r *http.Request, body []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		check(r, body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"what is the weather"}`))
		case strings.HasSuffix(r.URL.Path, "/audio/speech"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("MP3BYTES"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestTranscribeSendsMultipartAndReturnsText(t *testing.T) {
	srv := fakeOpenAI(t, func(r *http.Request, body []byte) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("missing bearer auth, got %q", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart upload, got %q", r.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(body), `name="model"`) || !strings.Contains(string(body), "whisper-1") {
			t.Errorf("expected whisper-1 model field in upload")
		}
		if !strings.Contains(string(body), `filename="audio.wav"`) {
			t.Errorf("expected the wav file part")
		}
	})
	defer srv.Close()

	eng := audio.NewOpenAIEngine(srv.URL, "sk-test")
	text, err := eng.Transcribe(context.Background(), []byte("RIFF....WAVE"), "audio/wav")
	if err != nil {
		t.Fatal(err)
	}
	if text != "what is the weather" {
		t.Fatalf("unexpected transcript %q", text)
	}
}

func TestSpeakReturnsAudioBytes(t *testing.T) {
	srv := fakeOpenAI(t, func(r *http.Request, body []byte) {
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("speech body is not JSON: %v", err)
		}
		if req["input"] != "hello there" || req["voice"] != "nova" || req["response_format"] != "mp3" {
			t.Errorf("unexpected speech request: %v", req)
		}
	})
	defer srv.Close()

	eng := audio.NewOpenAIEngine(srv.URL, "sk-test")
	data, mime, err := eng.Speak(context.Background(), "hello there", "nova")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "MP3BYTES" || mime != "audio/mpeg" {
		t.Fatalf("unexpected output %q (%s)", data, mime)
	}
}

func TestTranscribeRejectsUnknownAudioFormat(t *testing.T) {
	eng := audio.NewOpenAIEngine("http://127.0.0.1:1", "sk-test")
	if _, err := eng.Transcribe(context.Background(), []byte("x"), "application/octet-stream"); err == nil {
		t.Fatal("expected an unsupported format to be refused before any network call")
	}
}

func TestSpeakRejectsEmptyText(t *testing.T) {
	eng := audio.NewOpenAIEngine("http://127.0.0.1:1", "sk-test")
	if _, _, err := eng.Speak(context.Background(), "   ", ""); err == nil {
		t.Fatal("expected empty speech input to be refused")
	}
}

func TestProviderErrorsSurfaceWithStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	eng := audio.NewOpenAIEngine(srv.URL, "wrong")
	_, err := eng.Transcribe(context.Background(), []byte("RIFF"), "audio/wav")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected the 401 to surface, got %v", err)
	}
}
