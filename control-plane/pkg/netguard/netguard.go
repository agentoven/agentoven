// Package netguard keeps a tool from being pointed at the control plane's own network: the cloud
// metadata service, loopback, link-local and private ranges. A tool's endpoint is chosen by whoever
// registers it, and the control plane calls it from inside the deployment, so without a check
// "register a tool at http://169.254.169.254/latest/meta-data/" is a way to read the instance's credentials
// (server-side request forgery).
//
// The rule is applied twice. When a tool is registered, CheckURL refuses an endpoint whose host is one of
// these addresses, so the mistake is reported at once. Every call then dials through the guard
// (Transport, Client), which resolves the name itself and connects to the address it checked, so a name
// that answered with a public address at registration and a private one at call time (DNS rebinding) gets
// no further.
//
// Hosts the operator or a kitchen allowlists may be private: an on-premises MCP server is a normal
// thing to call. The cloud metadata addresses are refused even for an allowlisted host.
//
// The allowlist comes from AGENTOVEN_TOOL_EGRESS_ALLOW (comma separated host names, IP addresses or
// "*.suffix"), and an edition adds a per-kitchen list with SetKitchenAllow (Pro: the kitchen's egress
// allowlist). Proxy settings in the environment are ignored: a proxy would see, and could be steered to,
// any address.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// EnvAllow names the operator's allowlist for every kitchen.
const EnvAllow = "AGENTOVEN_TOOL_EGRESS_ALLOW"

// ErrRefused is wrapped by every refusal, so a caller can tell a policy decision from a network failure.
var ErrRefused = errors.New("destination not allowed")

var blocked = prefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
)

// metadata are the cloud instance-metadata addresses: refused even for an allowlisted host.
var metadata = prefixes("169.254.0.0/16", "fd00:ec2::254/128", "fe80::/10")

func prefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

func inAny(ip netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// IPBlocked reports whether an address may only be reached through an allowlisted host.
func IPBlocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	return !ip.IsValid() || !ip.IsGlobalUnicast() || inAny(ip, blocked)
}

// IPIsMetadata reports whether an address is a cloud metadata address.
func IPIsMetadata(ip netip.Addr) bool { return inAny(ip.Unmap(), metadata) }

// Guard applies the rule. The zero value works: it allowlists only what the environment names.
type Guard struct {
	// KitchenAllow returns the host names a kitchen has allowlisted. An error means "nothing".
	KitchenAllow func(ctx context.Context, kitchen string) ([]string, error)
	// Resolver looks names up; nil means net.DefaultResolver. Tests replace it.
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
	// Dialer connects once an address has passed; nil means one with a 10 s timeout.
	Dialer *net.Dialer
}

var (
	defaultMu sync.RWMutex
	def       = &Guard{}
)

// Default is the guard the control plane uses unless told otherwise.
func Default() *Guard {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return def
}

// SetKitchenAllow lets an edition add a per-kitchen allowlist to the default guard. Call it before serving.
func SetKitchenAllow(f func(ctx context.Context, kitchen string) ([]string, error)) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	def = &Guard{KitchenAllow: f, Resolver: def.Resolver, Dialer: def.Dialer}
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

func hostAllowed(list []string, host string) bool {
	host = normHost(host)
	for _, e := range list {
		e = normHost(e)
		switch {
		case e == "":
		case e == host:
			return true
		case strings.HasPrefix(e, "*.") && strings.HasSuffix(host, e[1:]) && len(host) > len(e)-1:
			return true
		}
	}
	return false
}

func envAllow() []string {
	var out []string
	for _, h := range strings.Split(os.Getenv(EnvAllow), ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func (g *Guard) allowed(ctx context.Context, kitchen, host string) bool {
	if hostAllowed(envAllow(), host) {
		return true
	}
	if g.KitchenAllow == nil {
		return false
	}
	list, err := g.KitchenAllow(ctx, kitchen)
	return err == nil && hostAllowed(list, host)
}

// resolve returns the addresses a host stands for.
func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	res := g.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	ips, err := res.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolving %s: no addresses", host)
	}
	return ips, nil
}

// vet resolves host and refuses it when any of its addresses may not be reached. Every address must
// pass: a name with one public and one private address would otherwise be a coin toss for an attacker.
func (g *Guard) vet(ctx context.Context, kitchen, host string) ([]netip.Addr, error) {
	ips, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	allowlisted := g.allowed(ctx, kitchen, host)
	for _, ip := range ips {
		switch {
		case IPIsMetadata(ip):
			return nil, fmt.Errorf("%w: %s resolves to a cloud metadata address, which no tool may reach", ErrRefused, host)
		case !allowlisted && IPBlocked(ip):
			return nil, fmt.Errorf("%w: %s resolves to a private or local address; allowlist the host for this kitchen (or set %s) first", ErrRefused, host, EnvAllow)
		}
	}
	return ips, nil
}

// CheckURL is the registration-time check: the endpoint must be an http(s) URL without credentials whose
// host passes. A name that does not resolve yet is not refused here (the tool may be registered before
// its server exists); the call-time check still applies to whatever it resolves to then.
func (g *Guard) CheckURL(ctx context.Context, kitchen, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a URL", endpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("a tool endpoint must be an http or https URL, not %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("a tool endpoint must not carry credentials in the URL; use the tool's authentication")
	}
	host := u.Hostname()
	if _, err := g.vet(ctx, kitchen, host); err != nil {
		var dns *net.DNSError
		if errors.As(err, &dns) {
			return nil
		}
		return err
	}
	return nil
}

// Dial connects to addr ("host:port") for kitchen, dialling the address it vetted.
func (g *Guard) Dial(ctx context.Context, kitchen, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not host:port", ErrRefused, addr)
	}
	ips, err := g.vet(ctx, kitchen, host)
	if err != nil {
		return nil, err
	}
	d := g.Dialer
	if d == nil {
		d = &net.Dialer{Timeout: 10 * time.Second}
	}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// Transport is an http.RoundTripper for kitchen that dials through the guard and ignores proxy settings.
func (g *Guard) Transport(kitchen string) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return g.Dial(ctx, kitchen, network, addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
	}
}

// Client is an http.Client for kitchen. A redirect is followed only to the same host (the dial check
// would stop a redirect to a private address anyway, and a credential must not be replayed elsewhere).
func (g *Guard) Client(kitchen string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: g.Transport(kitchen),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 || !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}
