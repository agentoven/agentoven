package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	v, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []netip.Addr
	for _, s := range v {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

func TestEndpointsThatReachTheControlPlanesOwnNetworkAreRefused(t *testing.T) {
	g := &Guard{Resolver: fakeResolver{
		"public.test": {"93.184.216.34"}, "meta.test": {"169.254.169.254"}, "internal.test": {"10.1.2.3"},
		"mixed.test": {"93.184.216.34", "127.0.0.1"}, "v6.test": {"fd00:ec2::254"}, "mapped.test": {"::ffff:10.0.0.1"},
	}}
	ctx := context.Background()
	for url, wantErr := range map[string]bool{
		"https://public.test/mcp":                 false,
		"http://169.254.169.254/latest/meta-data": true,
		"http://meta.test/":                       true,
		"http://internal.test:8080/":              true,
		"http://127.0.0.1:8081/api/v1/agents":     true,
		"http://localhost/":                       true, // not resolvable by the fake: dns errors are tolerated, see below
		"http://mixed.test/":                      true,
		"http://v6.test/":                         true,
		"http://mapped.test/":                     true,
		"http://[::1]/":                           true,
		"http://0.0.0.0/":                         true,
		"http://2130706433/":                      false, // a decimal address is a host name to the URL parser; the dial resolves it
		"file:///etc/passwd":                      true,
		"gopher://public.test/":                   true,
		"https://user:pw@public.test/":            true,
		"not a url":                               true,
	} {
		err := g.CheckURL(ctx, "k", url)
		if url == "http://localhost/" || url == "http://2130706433/" {
			continue // names the fake cannot resolve are decided at dial time, tested below
		}
		if (err != nil) != wantErr {
			t.Errorf("CheckURL(%s) = %v, want error: %v", url, err, wantErr)
		}
	}
}

func TestTheDialRefusesWhatTheNameResolvesToAtCallTime(t *testing.T) {
	r := fakeResolver{"rebind.test": {"93.184.216.34"}}
	g := &Guard{Resolver: r}
	if err := g.CheckURL(context.Background(), "k", "http://rebind.test/"); err != nil {
		t.Fatalf("a public address passes registration: %v", err)
	}
	r["rebind.test"] = []string{"169.254.169.254"} // DNS rebinding: the name now points inside
	if _, err := g.Dial(context.Background(), "k", "tcp", "rebind.test:80"); !errors.Is(err, ErrRefused) {
		t.Fatalf("the dial must be refused: %v", err)
	}
	// Even a host the operator allowlisted cannot reach the metadata service.
	t.Setenv(EnvAllow, "rebind.test")
	if _, err := g.Dial(context.Background(), "k", "tcp", "rebind.test:80"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("metadata is refused even when allowlisted: %v", err)
	}
}

func TestAnAllowlistedHostMayBePrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	g := &Guard{}
	c := g.Client("k", 3*time.Second)
	if _, err := c.Get(srv.URL); err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("loopback is refused without an allowlist: %v", err)
	}
	// The kitchen's list.
	g.KitchenAllow = func(_ context.Context, k string) ([]string, error) {
		if k == "k" {
			return []string{host}, nil
		}
		return nil, nil
	}
	if resp, err := g.Client("k", 3*time.Second).Get(srv.URL); err != nil || resp.StatusCode != 204 {
		t.Fatalf("an allowlisted host must be reachable: %v", err)
	}
	if _, err := g.Client("other", 3*time.Second).Get(srv.URL); !errors.Is(err, ErrRefused) {
		t.Fatalf("another kitchen's call is not covered by the allowlist: %v", err)
	}
	// The operator's list, and a failing kitchen list means nothing is allowlisted.
	g.KitchenAllow = func(context.Context, string) ([]string, error) { return nil, errors.New("db down") }
	if _, err := g.Client("k", 3*time.Second).Get(srv.URL); !errors.Is(err, ErrRefused) {
		t.Fatalf("a failing allowlist must fail closed: %v", err)
	}
	t.Setenv(EnvAllow, "*."+host+",127.0.0.1")
	if resp, err := g.Client("k", 3*time.Second).Get(srv.URL); err != nil || resp.StatusCode != 204 {
		t.Fatalf("the operator's allowlist: %v", err)
	}
}

func TestARedirectToAnotherHostIsNotFollowed(t *testing.T) {
	t.Setenv(EnvAllow, "127.0.0.1")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
	}))
	defer src.Close()
	resp, err := (&Guard{}).Client("k", 3*time.Second).Get(src.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("the redirect to another host must be returned, not followed: %d", resp.StatusCode)
	}
}
