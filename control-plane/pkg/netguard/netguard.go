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
	"regexp"
	"strings"
	"sync"
	"time"
)

// EnvAllow names the operator's allowlist for tools, for every kitchen.
const EnvAllow = "AGENTOVEN_TOOL_EGRESS_ALLOW"

// EnvChannelAllow names the operator's allowlist for notification channels (a webhook, Slack, Teams, Discord,
// Zapier, an e-mail channel's SMTP host). It is separate from EnvAllow (owner decision S10, 2026-11-03): a
// private host a tool may call (an internal MCP server) is not thereby a place notifications, which carry
// what the run produced, may be sent, and the other way round.
const EnvChannelAllow = "AGENTOVEN_CHANNEL_EGRESS_ALLOW"

// ErrRefused is wrapped by every refusal, so a caller can tell a policy decision from a network failure.
var ErrRefused = errors.New("destination not allowed")

var blocked = prefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32",
	"2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
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

// canon is the address the prefix tables are asked about: no zone and no IPv4-in-IPv6 wrapper. A
// netip.Prefix never contains an address that carries a zone ("fd00:ec2::254%eth0"), so without this
// a zoned literal would walk past every range below while the operating system still connects to it.
func canon(ip netip.Addr) netip.Addr { return ip.WithZone("").Unmap() }

// IPBlocked reports whether an address may only be reached through an allowlisted host. An address
// that carries a zone is blocked whatever it is: a zone names a local interface, which no tool has a
// reason to reach.
func IPBlocked(ip netip.Addr) bool {
	if ip.Zone() != "" {
		return true
	}
	ip = canon(ip)
	return !ip.IsValid() || !ip.IsGlobalUnicast() || inAny(ip, blocked)
}

// IPIsMetadata reports whether an address is a cloud metadata address, with or without a zone.
func IPIsMetadata(ip netip.Addr) bool { return inAny(canon(ip), metadata) }

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
	// Allow is a fixed list an installation sets for this guard alone, in the same spelling as the
	// environment's list and also with CIDR ranges ("10.5.0.0/16"). Unlike the other lists, an address
	// or range on it admits any host name that resolves into it (an on-premises mail server by name).
	Allow []string
	// IgnoreEnv leaves AGENTOVEN_TOOL_EGRESS_ALLOW out: a guard with its own installation list (Allow)
	// is not widened by the setting that belongs to tools.
	IgnoreEnv bool
	// EnvName is the environment variable holding the operator's list for this guard; empty means
	// AGENTOVEN_TOOL_EGRESS_ALLOW. The channel guard reads AGENTOVEN_CHANNEL_EGRESS_ALLOW.
	EnvName string
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
	def = &Guard{KitchenAllow: f, Resolver: def.Resolver, Dialer: def.Dialer, Allow: def.Allow, IgnoreEnv: def.IgnoreEnv, EnvName: def.EnvName}
}

var channels = &Guard{EnvName: EnvChannelAllow}

// Channels is the guard notification channels send through: the same rules as Default, with its own
// allowlists, AGENTOVEN_CHANNEL_EGRESS_ALLOW for the operator and the list SetChannelKitchenAllow installs for
// the kitchen. Neither list is shared with tools (owner decision S10).
func Channels() *Guard {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return channels
}

// SetChannelKitchenAllow lets an edition add a per-kitchen channel allowlist. Call it before serving.
func SetChannelKitchenAllow(f func(ctx context.Context, kitchen string) ([]string, error)) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	channels = &Guard{KitchenAllow: f, Resolver: channels.Resolver, Dialer: channels.Dialer, Allow: channels.Allow, IgnoreEnv: channels.IgnoreEnv, EnvName: channels.EnvName}
}

// NormHost is a host name as lists compare it: lower case, without a trailing dot.
func NormHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

// HostAllowed says whether host matches an allowlist entry: an exact name or address, or "*.suffix" for
// any sub-domain.
func HostAllowed(list []string, host string) bool {
	host = NormHost(host)
	for _, e := range list {
		e = NormHost(e)
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

// envName is the variable the operator's list for this guard is read from.
func (g *Guard) envName() string {
	if g.EnvName == "" {
		return EnvAllow
	}
	return g.EnvName
}

func (g *Guard) envAllow() []string {
	var out []string
	for _, h := range strings.Split(os.Getenv(g.envName()), ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// allowed says whether host is on a list that admits a name: the environment's, the kitchen's, or the
// guard's own.
func (g *Guard) allowed(ctx context.Context, kitchen, host string) bool {
	if !g.IgnoreEnv && HostAllowed(g.envAllow(), host) {
		return true
	}
	if HostAllowed(g.Allow, host) {
		return true
	}
	if g.KitchenAllow == nil {
		return false
	}
	list, err := g.KitchenAllow(ctx, kitchen)
	return err == nil && HostAllowed(list, host)
}

// allowsAddr says whether the guard's own list admits an address by number or by range.
func (g *Guard) allowsAddr(ip netip.Addr) bool {
	ip = canon(ip)
	for _, e := range g.Allow {
		e = strings.TrimSpace(e)
		if p, err := netip.ParsePrefix(e); err == nil && p.Contains(ip) {
			return true
		}
		if a, err := netip.ParseAddr(e); err == nil && canon(a) == ip {
			return true
		}
	}
	return false
}

// numericHost is a host written the way inet_aton accepts and netip does not: 2130706433, 0x7f.1,
// 017700000001, 127.1. Some resolvers turn these into an address (127.0.0.1) and others look them up as
// names; a tool has no reason to spell a host so, and refusing them removes the ambiguity.
var numericHost = regexp.MustCompile(`^(0[xX][0-9a-fA-F]*|[0-9]+)(\.(0[xX][0-9a-fA-F]*|[0-9]+))*$`)

// resolve returns the addresses a host stands for. A literal is taken as it is; anything else is a name
// and goes to the resolver. It refuses the spellings that would make "which address is this" depend on
// who is asked: a zone identifier, an inet_aton number, a name that is not a plain host.
func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.ToLower(strings.TrimSuffix(host, ".")) // "127.0.0.1." and "localhost." are the same hosts
	switch {
	case host == "":
		return nil, fmt.Errorf("%w: empty host", ErrRefused)
	case strings.ContainsAny(host, "%@/\\?# \t\r\n"):
		return nil, fmt.Errorf("%w: %q is not a plain host name or address (zone identifiers and userinfo are refused)", ErrRefused, host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if numericHost.MatchString(host) {
		return nil, fmt.Errorf("%w: %q is a number written in an ambiguous form; use the dotted address", ErrRefused, host)
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

// Vet resolves host and refuses it when any of its addresses may not be reached. Every address must
// pass: a name with one public and one private address would otherwise be a coin toss for an attacker.
// It is what a registration-time check and every dial both go through, so an edition that has its own
// place to check a host (a trigger's mail server) asks the same question the dial will.
func (g *Guard) Vet(ctx context.Context, kitchen, host string) ([]netip.Addr, error) {
	ips, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	allowlisted := g.allowed(ctx, kitchen, host)
	for _, ip := range ips {
		switch {
		case IPIsMetadata(ip):
			return nil, fmt.Errorf("%w: %s resolves to a cloud metadata address, which nothing may reach", ErrRefused, host)
		case ip.Zone() != "":
			return nil, fmt.Errorf("%w: %s resolves to an address with a zone identifier", ErrRefused, host)
		case !allowlisted && !g.allowsAddr(ip) && IPBlocked(ip):
			return nil, fmt.Errorf("%w: %s resolves to a private or local address; allowlist the host for this kitchen (or set %s) first", ErrRefused, host, g.envName())
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
	if _, err := g.Vet(ctx, kitchen, u.Hostname()); err != nil {
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
	ips, err := g.Vet(ctx, kitchen, host)
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

// HTTPOptions bound a client.
type HTTPOptions struct {
	// Timeout is the whole call, headers and body; 0 means 30 s.
	Timeout time.Duration
	// MaxRedirects is how many redirects are followed (each only to the same host, never from https to
	// http); 0 follows none.
	MaxRedirects int
	// NoKeepAlive makes every call its own connection, so nothing is held between calls or kitchens.
	NoKeepAlive bool
}

// ClientWith is an http.Client for kitchen that follows the guard. A redirect elsewhere is returned to
// the caller as it is rather than followed, so a credential sent to one host is never replayed to another.
func (g *Guard) ClientWith(kitchen string, o HTTPOptions) *http.Client {
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	tr := g.Transport(kitchen)
	tr.ResponseHeaderTimeout = o.Timeout
	if o.NoKeepAlive {
		tr.DisableKeepAlives, tr.MaxIdleConns = true, 1
	}
	return &http.Client{
		Timeout:   o.Timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			first := via[0].URL
			if len(via) > o.MaxRedirects || !strings.EqualFold(req.URL.Host, first.Host) || (first.Scheme == "https" && req.URL.Scheme != "https") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// Client is ClientWith for a tool call: up to three redirects, connections kept alive.
func (g *Guard) Client(kitchen string, timeout time.Duration) *http.Client {
	return g.ClientWith(kitchen, HTTPOptions{Timeout: timeout, MaxRedirects: 3})
}
