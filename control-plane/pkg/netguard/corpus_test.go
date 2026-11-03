package netguard

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
)

// The corpus below is the one list of "ways to name an address you must not reach". Both of Pro's
// callers (a trigger's mail server and object store, a tool pack's connections) and every MCP tool
// endpoint reach the network through this package, so a spelling added here is tested for all of them.

var corpusResolver = fakeResolver{
	"public.test":   {"93.184.216.34"},
	"internal.test": {"10.1.2.3"},
	"meta.test":     {"169.254.169.254"},
	"v6meta.test":   {"fd00:ec2::254"},
	"mixed.test":    {"93.184.216.34", "10.0.0.9"},
	"mapped.test":   {"::ffff:10.0.0.1"},
	"nat64.test":    {"64:ff9b::a00:1"},
	"loopback.test": {"127.0.0.1"},
}

func TestEveryWayToNameAnAddressThatMustNotBeReachedIsRefused(t *testing.T) {
	g := &Guard{Resolver: corpusResolver}
	ctx := context.Background()
	refused := []string{
		// IPv4 inside IPv6, in every spelling
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:7f00:1", "::ffff:169.254.169.254", "::ffff:a9fe:a9fe", "mapped.test",
		// IPv4-compatible, NAT64, 6to4 and Teredo carry an IPv4 address inside an IPv6 one
		"::10.0.0.1", "::7f00:1", "64:ff9b::a00:1", "64:ff9b::7f00:1", "64:ff9b:1::1", "2002:a00:1::1", "2002:7f00:1::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "nat64.test",
		// zone identifiers: a zoned address walks past every prefix table and the OS still connects to it
		"fe80::1%eth0", "fd00:ec2::254%eth0", "fd12::1%en0", "::1%lo0", "2606:4700::1111%eth0", "::ffff:10.0.0.1%eth0", "fe80::1%25eth0",
		// the same hosts as numbers written the way inet_aton reads them
		"2130706433", "0x7f000001", "0x7f.0.0.1", "017700000001", "0177.0.0.1", "127.1", "10.1", "0xa9fea9fe", "2852039166", "0", "0x0", "00000",
		// trailing dots, case
		"localhost.", "127.0.0.1.", "169.254.169.254.", "LOOPBACK.TEST", "internal.test.", "meta.test.",
		// plain private and special-use ranges and names
		"127.0.0.1", "10.0.0.1", "172.16.5.5", "192.168.1.1", "100.64.0.1", "169.254.169.254", "169.254.170.2", "0.0.0.0", "224.0.0.1", "255.255.255.255", "198.18.0.1",
		"::1", "::", "fd00:ec2::254", "fc00::1", "fe80::1", "ff02::1", "internal.test", "meta.test", "v6meta.test", "mixed.test", "loopback.test",
		// not a host at all
		"", ".", "user@public.test", "public.test/x", "public.test?x", "public.test#x", "pub lic.test", "public.test\r\nHost: x", "a\\b",
		// full-width digits and IDN look-alikes are names the resolver does not know: they must not fall through to an address
		"１２７.０.０.１", "ｌｏｃａｌｈｏｓｔ", "xn--127-0-0-1.test",
	}
	for _, h := range refused {
		if _, err := g.Vet(ctx, "k", h); err == nil {
			t.Errorf("Vet(%q) was allowed", h)
		}
		if _, err := g.Dial(ctx, "k", "tcp", "["+h+"]:80"); err == nil {
			t.Errorf("Dial([%s]:80) was allowed", h)
		}
	}
	for _, h := range []string{"public.test", "PUBLIC.TEST", "public.test.", "93.184.216.34", "2606:4700::1111", "::ffff:93.184.216.34"} {
		if _, err := g.Vet(ctx, "k", h); err != nil {
			t.Errorf("Vet(%q) = %v, want allowed", h, err)
		}
	}
}

func TestTheZoneRuleHoldsForTheTablesThemselves(t *testing.T) {
	for _, s := range []string{"fd00:ec2::254%eth0", "fe80::1%lo0", "fd12::1%en0", "::ffff:10.0.0.1%eth0", "2606:4700::1111%eth0"} {
		ip := netip.MustParseAddr(s)
		if !IPBlocked(ip) {
			t.Errorf("IPBlocked(%s) = false: an address with a zone is never reachable", s)
		}
	}
	if !IPIsMetadata(netip.MustParseAddr("fd00:ec2::254%eth0")) || !IPIsMetadata(netip.MustParseAddr("169.254.169.254")) {
		t.Error("the metadata check must see through a zone")
	}
}

func TestEndpointURLsWithCredentialsOrOddHostsAreRefusedAtRegistration(t *testing.T) {
	g := &Guard{Resolver: corpusResolver}
	for _, raw := range []string{
		"http://user@public.test/", "http://user:pw@public.test/", "http://127.0.0.1@public.test/", "http://public.test@127.0.0.1/",
		"http://[fd00:ec2::254%25eth0]/", "http://[fe80::1%25en0]:8080/", "http://0x7f.0.0.1/", "http://0177.0.0.1/", "http://2130706433/",
		"http://127.1/", "http://127.0.0.1./", "http://[::ffff:127.0.0.1]/", "http://[::ffff:7f00:1]:9/",
		"http://meta.test./", "ftp://public.test/", "file:///etc/passwd", "gopher://public.test/", "//public.test/",
	} {
		if err := g.CheckURL(context.Background(), "k", raw); err == nil {
			t.Errorf("CheckURL(%q) was allowed", raw)
		}
	}
}

func TestAnAllowedListNeverOpensTheMetadataAddressOrAZone(t *testing.T) {
	g := &Guard{Resolver: corpusResolver, Allow: []string{"meta.test", "169.254.0.0/16", "fe80::/10", "fd00:ec2::254", "internal.test", "10.0.0.0/8"}, IgnoreEnv: true}
	ctx := context.Background()
	for _, h := range []string{"meta.test", "169.254.169.254", "fd00:ec2::254", "v6meta.test", "fe80::1%eth0", "fd00:ec2::254%eth0"} {
		if _, err := g.Vet(ctx, "k", h); err == nil {
			t.Errorf("Vet(%q) must be refused even when it is on the guard's list", h)
		}
	}
	for _, h := range []string{"internal.test", "10.9.9.9", "mixed.test"} {
		_, err := g.Vet(ctx, "k", h)
		if h != "mixed.test" && err != nil {
			t.Errorf("Vet(%q) = %v: the guard's own list admits it", h, err)
		}
		if h == "mixed.test" && err != nil {
			t.Errorf("mixed.test has a public and a 10/8 address, both admitted by this list: %v", err)
		}
	}
	// A guard with its own list is not widened by the tools' environment setting.
	t.Setenv(EnvAllow, "loopback.test")
	if _, err := g.Vet(ctx, "k", "loopback.test"); err == nil {
		t.Error("IgnoreEnv must keep AGENTOVEN_TOOL_EGRESS_ALLOW out")
	}
	if _, err := (&Guard{Resolver: corpusResolver}).Vet(ctx, "k", "loopback.test"); err != nil {
		t.Errorf("without IgnoreEnv the environment list applies: %v", err)
	}
}

func TestRedirectsStayOnTheHostAndNeverDowngrade(t *testing.T) {
	c := (&Guard{}).ClientWith("k", HTTPOptions{MaxRedirects: 2})
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	for _, tc := range []struct {
		name  string
		to    string
		via   []string
		allow bool
	}{
		{"same host", "https://a.test/b", []string{"https://a.test/a"}, true},
		{"other host", "https://evil.test/b", []string{"https://a.test/a"}, false},
		{"other port", "https://a.test:8443/b", []string{"https://a.test/a"}, false},
		{"userinfo host trick", "https://a.test@evil.test/b", []string{"https://a.test/a"}, false},
		{"downgrade", "http://a.test/b", []string{"https://a.test/a"}, false},
		{"too many", "https://a.test/d", []string{"https://a.test/a", "https://a.test/b", "https://a.test/c"}, false},
		{"loopback", "http://127.0.0.1/", []string{"https://a.test/a"}, false},
	} {
		var via []*http.Request
		for _, v := range tc.via {
			via = append(via, req(v))
		}
		if got := c.CheckRedirect(req(tc.to), via) == nil; got != tc.allow {
			t.Errorf("%s: followed = %v, want %v", tc.name, got, tc.allow)
		}
	}
}
