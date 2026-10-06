package middleware

import (
	"net/http"
	"strings"
)

// SkipForUpgrade applies mw to every request except protocol upgrades
// (WebSocket). chi's Compress wraps the ResponseWriter in a type that cannot be
// hijacked, so a WebSocket handshake behind it fails with a 501; an upgraded
// connection is not an HTTP body to compress anyway.
func SkipForUpgrade(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}
