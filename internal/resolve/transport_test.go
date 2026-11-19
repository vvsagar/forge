package resolve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/git-pkgs/forge/internal/config"
)

// writeUserConfig points config.Load at a temporary user config containing
// content and clears the cached config before and after the test.
func writeUserConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "forge"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge", "config"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	config.ResetCache()
	t.Cleanup(config.ResetCache)
}

func TestHTTPClientSetsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := HTTPClient()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got != userAgent {
		t.Errorf("expected User-Agent %q, got %q", userAgent, got)
	}
	if got == "" {
		t.Error("User-Agent was empty")
	}
}

func TestHTTPClientUsesConfiguredProxyPerHost(t *testing.T) {
	var mu sync.Mutex
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.Host)
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	writeUserConfig(t, fmt.Sprintf("[proxied.test]\nproxy = %s\n", proxy.URL))

	c := HTTPClient()

	// The configured host goes through the proxy, which answers without
	// forwarding, so this also works without DNS for proxied.test.
	resp, err := c.Get("http://proxied.test/resource")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected response from proxy, got status %d", resp.StatusCode)
	}

	mu.Lock()
	got := append([]string(nil), proxied...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "proxied.test" {
		t.Errorf("expected one request for proxied.test through the proxy, got %v", got)
	}

	// Hosts without a proxy setting keep going direct.
	var directHits int
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits++
	}))
	defer direct.Close()
	resp, err = c.Get(direct.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if directHits != 1 {
		t.Errorf("expected direct request for unconfigured host, got %d hits", directHits)
	}
	mu.Lock()
	n := len(proxied)
	mu.Unlock()
	if n != 1 {
		t.Errorf("unconfigured host used the proxy: %d requests", n)
	}
}

func TestHTTPClientProxyMatchesHostWithPort(t *testing.T) {
	var mu sync.Mutex
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.Host)
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()
	hostPort := strings.TrimPrefix(target.URL, "http://")

	writeUserConfig(t, fmt.Sprintf("[%s]\nproxy = %s\n", hostPort, proxy.URL))

	c := HTTPClient()
	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected response from proxy, got status %d", resp.StatusCode)
	}

	mu.Lock()
	got := append([]string(nil), proxied...)
	mu.Unlock()
	if len(got) != 1 || got[0] != hostPort {
		t.Errorf("expected request for %s through the proxy, got %v", hostPort, got)
	}
}

func TestSetUserAgent(t *testing.T) {
	old := userAgent
	defer func() { userAgent = old }()

	SetUserAgent("forge/1.2.3")

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := HTTPClient()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got != "forge/1.2.3" {
		t.Errorf("expected User-Agent forge/1.2.3, got %q", got)
	}
}

func TestUserAgentTransportPreservesExisting(t *testing.T) {
	// If a caller has already set User-Agent (e.g. forge api -H "User-Agent: x"),
	// the transport must not stomp on it.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := HTTPClient()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("User-Agent", "custom/1.0")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got != "custom/1.0" {
		t.Errorf("expected explicit User-Agent to be preserved, got %q", got)
	}
}

func TestUserAgentTransportDoesNotMutateRequest(t *testing.T) {
	// RoundTrippers must not modify the original request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c := HTTPClient()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if req.Header.Get("User-Agent") != "" {
		t.Errorf("transport mutated the original request: User-Agent=%q", req.Header.Get("User-Agent"))
	}
}

// tlsHTTPClient returns HTTPClient with its transport trusting srv's
// self-signed certificate, keeping the redirect policy under test.
func tlsHTTPClient(srv *httptest.Server) *http.Client {
	c := HTTPClient()
	c.Transport = &userAgentTransport{base: srv.Client().Transport}
	return c
}

func TestHTTPClientRejectsHTTPSToHTTPRedirect(t *testing.T) {
	var plainHits int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization header sent over plain HTTP")
		}
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/target", http.StatusFound)
	}))
	defer secure.Close()

	req, _ := http.NewRequest(http.MethodGet, secure.URL, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := tlsHTTPClient(secure).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected HTTPS to HTTP redirect to be refused")
	}
	if !errors.Is(err, errInsecureRedirect) {
		t.Errorf("error = %v, want errInsecureRedirect", err)
	}
	if plainHits != 0 {
		t.Errorf("plain HTTP server was contacted %d times", plainHits)
	}
}

func TestHTTPClientFollowsSafeRedirects(t *testing.T) {
	t.Run("https to https", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, srv.URL+"/end", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()

		resp, err := tlsHTTPClient(srv).Get(srv.URL + "/start")
		if err != nil {
			t.Fatalf("redirect within HTTPS failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.Request.URL.Path != "/end" {
			t.Errorf("final path = %q, want /end", resp.Request.URL.Path)
		}
	})

	t.Run("http to https", func(t *testing.T) {
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer secure.Close()
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, secure.URL+"/end", http.StatusFound)
		}))
		defer plain.Close()

		resp, err := tlsHTTPClient(secure).Get(plain.URL)
		if err != nil {
			t.Fatalf("upgrade redirect failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.Request.URL.Scheme != "https" {
			t.Errorf("final scheme = %q, want https", resp.Request.URL.Scheme)
		}
	})

	t.Run("http to http", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, srv.URL+"/end", http.StatusFound)
			}
		}))
		defer srv.Close()

		resp, err := HTTPClient().Get(srv.URL + "/start")
		if err != nil {
			t.Fatalf("redirect within HTTP failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.Request.URL.Path != "/end" {
			t.Errorf("final path = %q, want /end", resp.Request.URL.Path)
		}
	})
}

func TestHTTPClientKeepsRedirectLimit(t *testing.T) {
	var hits int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := HTTPClient().Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected redirect loop to stop")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("stopped after %d redirects", maxRedirects)) {
		t.Errorf("unexpected error: %v", err)
	}
	if hits != maxRedirects {
		t.Errorf("server hit %d times, want %d", hits, maxRedirects)
	}
}

// TestDetectedForgeUsesSharedHTTPClient covers a self-hosted forge with no
// type in config, so the client is built by RegisterDomain after probing.
// Both the probes and the backend must use HTTPClient, which means they get
// the User-Agent and refuse the HTTPS to HTTP redirect.
func TestDetectedForgeUsesSharedHTTPClient(t *testing.T) {
	config.ResetCache()
	t.Cleanup(config.ResetCache)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FORGE_HOST", "")
	t.Setenv("FORGE_TOKEN", "detected-secret")
	t.Chdir(t.TempDir())

	oldH, oldS, oldT := hostOverride, schemeOverride, forgeTypeOverride
	t.Cleanup(func() { hostOverride, schemeOverride, forgeTypeOverride = oldH, oldS, oldT })
	hostOverride, schemeOverride, forgeTypeOverride = "", "", ""

	var plainHits int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("token sent over plain HTTP")
		}
	}))
	defer plain.Close()

	var mu sync.Mutex
	var agents []string
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		agents = append(agents, r.URL.Path+" "+r.Header.Get("User-Agent"))
		mu.Unlock()
		if r.URL.Path == "/" {
			w.Header().Set("X-Gitea-Version", "1.22.0")
			return
		}
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer secure.Close()

	// HTTPClient wraps http.DefaultTransport, so point that at a transport
	// trusting the test server's certificate. This also lets a nil client
	// (http.DefaultClient) reach the server, which is the regression.
	oldTransport := http.DefaultTransport
	http.DefaultTransport = secure.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	domain := strings.TrimPrefix(secure.URL, "https://")
	f, err := ForgeForDomain(domain)
	if err != nil {
		t.Fatalf("ForgeForDomain: %v", err)
	}

	_, err = f.Repos().Get(context.Background(), "owner", "repo")
	if !errors.Is(err, errInsecureRedirect) {
		t.Errorf("Repos().Get error = %v, want errInsecureRedirect", err)
	}
	if plainHits != 0 {
		t.Errorf("plain HTTP server was contacted %d times", plainHits)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(agents) == 0 {
		t.Fatal("no requests reached the forge")
	}
	for _, a := range agents {
		if !strings.HasSuffix(a, " "+userAgent) {
			t.Errorf("request %q did not use the shared User-Agent %q", a, userAgent)
		}
	}
}
