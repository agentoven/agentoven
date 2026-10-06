package middleware

import (
	"bufio"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Unwrap lets http.NewResponseController (and the WebSocket library) reach the
// real connection. Without it this wrapper hides Hijack, so a WebSocket upgrade
// anywhere below the logger is refused with a 501.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *responseWriter) Flush() { _ = http.NewResponseController(rw.ResponseWriter).Flush() }

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(rw.ResponseWriter).Hijack()
}

// Logger returns structured request logging middleware.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := newResponseWriter(w)

		next.ServeHTTP(rw, r)

		duration := time.Since(start)

		event := log.Info()
		if rw.statusCode >= 400 {
			event = log.Warn()
		}
		if rw.statusCode >= 500 {
			event = log.Error()
		}

		event.
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rw.statusCode).
			Int("bytes", rw.bytes).
			Dur("duration", duration).
			Str("remote", r.RemoteAddr).
			Str("user_agent", r.UserAgent()).
			Msg("request")
	})
}
