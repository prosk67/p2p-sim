// Package guard protects a node's HTTP API from callers that are not part of
// the pool:
//
//   - Host allow-list. A request whose Host is a DNS name the node does not
//     know is refused with 421, which defeats DNS rebinding: a hostile page
//     can point its own domain at 127.0.0.1, but the browser still sends that
//     domain as Host. IP literals, localhost, *.ts.net (Tailscale names, which
//     outsiders cannot rebind) and the peer list's hostnames are allowed.
//   - Browser cross-site check. Browsers mark cross-site requests with
//     Sec-Fetch-Site and Origin; such requests are refused with 403. Peers
//     and the gateway are Go clients and send neither header.
//   - Optional pool token. When set, every request except GET /health must
//     carry "Authorization: Bearer <token>". Transport adds it to the node's
//     own outbound calls.
package guard

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Options configures Handler.
type Options struct {
	// Token is the shared pool token; empty disables the token check.
	Token string
	// Hosts are extra allowed hostnames (no port), e.g. the peer list's.
	Hosts []string
}

// Handler wraps next with the checks described in the package comment.
func Handler(opts Options, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, h := range opts.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			allowed[h] = true
		}
	}
	want := []byte("Bearer " + opts.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HostAllowed(r.Host, allowed) {
			deny(w, http.StatusMisdirectedRequest, "unknown host "+r.Host)
			return
		}
		if CrossSite(r) {
			deny(w, http.StatusForbidden, "cross-site requests are not allowed")
			return
		}
		if opts.Token != "" && !(r.Method == http.MethodGet && r.URL.Path == "/health") &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="p2p-sim pool"`)
			deny(w, http.StatusUnauthorized, "missing or invalid pool token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HostAllowed reports whether a request's Host header names this service.
func HostAllowed(hostport string, extra map[string]bool) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	switch {
	case host == "":
		return false
	case net.ParseIP(host) != nil:
		return true
	case host == "localhost", strings.HasSuffix(host, ".localhost"):
		return true
	case strings.HasSuffix(host, ".ts.net"):
		return true
	}
	return extra[host]
}

// CrossSite reports whether a browser marked r as coming from another site
// or origin. Requests without these headers (curl, Go clients) pass.
func CrossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true // includes "null"
	}
	return !strings.EqualFold(u.Host, r.Host)
}

// HostsFromURLs returns the hostnames of the given URLs, for Options.Hosts.
func HostsFromURLs(urls []string) []string {
	var out []string
	for _, raw := range urls {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			out = append(out, u.Hostname())
		}
	}
	return out
}

// SplitList splits a comma-separated environment value.
func SplitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Transport adds the pool token to every outbound request. Peer clients never
// follow redirects, so the token only goes to configured peers.
type Transport struct {
	Token string
	Base  http.RoundTripper
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Token == "" {
		return base.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.Token)
	return base.RoundTrip(r)
}

func deny(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
