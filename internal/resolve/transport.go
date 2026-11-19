package resolve

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/git-pkgs/forge"
	"github.com/git-pkgs/forge/internal/config"
)

// maxRedirects matches the limit net/http applies when CheckRedirect is nil.
const maxRedirects = 10

var userAgent = "forge/dev"

// SetUserAgent sets the User-Agent string sent on every HTTP request made
// through HTTPClient. The CLI calls this at startup with the build version.
func SetUserAgent(ua string) {
	userAgent = ua
}

var readOnly bool

// SetReadOnly makes HTTPClient refuse requests that could modify forge
// state. The CLI calls this from the --read-only flag and FORGE_READ_ONLY.
func SetReadOnly(on bool) {
	readOnly = on
}

// ReadOnly reports whether read-only mode was enabled with SetReadOnly.
func ReadOnly() bool {
	return readOnly
}

// HTTPClient returns an http.Client whose transport sets the User-Agent
// header on outbound requests. Used for all forge API traffic so requests
// are identifiable in server logs. It refuses redirects from HTTPS to HTTP,
// since net/http would otherwise resend headers such as Authorization in
// cleartext when the redirect stays on the same host. In read-only mode the
// client only sends GET, HEAD and OPTIONS requests.
func HTTPClient() *http.Client {
	hc := &http.Client{
		Transport:     &userAgentTransport{base: baseTransport()},
		CheckRedirect: checkRedirect,
	}
	if readOnly {
		return forges.ReadOnlyHTTPClient(hc)
	}
	return hc
}

// baseTransport clones http.DefaultTransport, keeping its timeouts and TLS
// settings, and replaces its proxy policy. One transport is returned per
// client, so all forge backends and host detection on that client share a
// connection pool.
func baseTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{Proxy: proxyForRequest}
	}
	t = t.Clone()
	t.Proxy = proxyForRequest
	return t
}

// proxyForRequest picks the proxy for a request. A proxy configured for the
// request's host in the user config wins; otherwise the standard HTTP_PROXY,
// HTTPS_PROXY and NO_PROXY environment variables apply.
func proxyForRequest(req *http.Request) (*url.URL, error) {
	if u := configuredProxy(req.URL); u != nil {
		return u, nil
	}
	return http.ProxyFromEnvironment(req)
}

// configuredProxy returns the proxy URL configured for a request's host, or
// nil when the host has no proxy setting. Section names may include a port, so
// the host is matched both with and without it.
func configuredProxy(u *url.URL) *url.URL {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return nil
	}
	host := strings.ToLower(u.Host)
	ds, ok := cfg.Domains[host]
	if !ok {
		if name := strings.ToLower(u.Hostname()); name != host {
			ds, ok = cfg.Domains[name]
		}
	}
	if !ok || ds.Proxy == "" {
		return nil
	}
	proxy, err := url.Parse(ds.Proxy)
	if err != nil {
		return nil
	}
	return proxy
}

// errInsecureRedirect is returned when a redirect would downgrade from
// HTTPS to HTTP. The message names no URL, because net/http already wraps
// it in a *url.Error that carries the redirect target.
var errInsecureRedirect = errors.New("refusing redirect from https to insecure http")

// checkRedirect keeps the default redirect limit and rejects any hop that
// downgrades from HTTPS to HTTP.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return errInsecureRedirect
	}
	return nil
}

type userAgentTransport struct {
	base http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") != "" {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", userAgent)
	return t.base.RoundTrip(r)
}
