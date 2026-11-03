package router

import (
	"fmt"
	"net/http"
)

// refuseCrossHostRedirect is the CheckRedirect of every provider client. A provider call carries the
// provider's credential in a header (x-api-key, api-key, Authorization), and Go forwards custom headers
// to wherever a redirect points. A provider's endpoint is the host its credential was given for, so a
// response that redirects elsewhere is refused instead of followed: the credential never leaves that host.
// Redirects within the same host (a trailing-slash or path move) are still followed.
func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
		return fmt.Errorf("provider redirected to another host (%s): refused, its credential is only sent to the endpoint it was given for", req.URL.Host)
	}
	return nil
}
