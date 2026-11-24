package guard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(opts Options, r *http.Request) int {
	h := Handler(opts, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func TestHostAllowList(t *testing.T) {
	opts := Options{Hosts: []string{"node-2", "Lab-PC.lan"}}
	cases := map[string]int{
		"127.0.0.1:8081":           200,
		"[::1]:8081":               200,
		"100.64.0.2:8082":          200,
		"localhost:8081":           200,
		"node-2:8000":              200,
		"lab-pc.lan":               200,
		"lab-pc-1.tail1234.ts.net": 200,
		"evil.example:8081":        421,
		"node-2.evil.example:8000": 421,
		"":                         421,
	}
	for host, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/status", nil)
		r.Host = host
		if got := serve(opts, r); got != want {
			t.Errorf("Host %q: got %d, want %d", host, got, want)
		}
	}
}

func TestCrossSiteBrowserRequestsRefused(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"go client", nil, 200},
		{"same origin", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8081"}, 200},
		{"typed url", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"cross-site form", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"}, 403},
		{"same-site other port", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"old browser, foreign origin", map[string]string{"Origin": "http://evil.example"}, 403},
		{"opaque origin", map[string]string{"Origin": "null"}, 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/run", nil)
		r.Host = "127.0.0.1:8081"
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := serve(Options{}, r); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestPoolToken(t *testing.T) {
	opts := Options{Token: "s3cret"}
	req := func(method, path, auth string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.Host = "127.0.0.1"
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	if got := serve(opts, req(http.MethodGet, "/health", "")); got != 200 {
		t.Fatalf("health must stay open, got %d", got)
	}
	if got := serve(opts, req(http.MethodPost, "/partition/x/step", "")); got != 401 {
		t.Fatalf("no token: got %d", got)
	}
	if got := serve(opts, req(http.MethodPost, "/partition/x/step", "Bearer wrong")); got != 401 {
		t.Fatalf("wrong token: got %d", got)
	}
	if got := serve(opts, req(http.MethodPost, "/partition/x/step", "Bearer s3cret")); got != 200 {
		t.Fatalf("right token: got %d", got)
	}
}

func TestTransportAddsToken(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Get("Authorization") }))
	defer srv.Close()
	client := &http.Client{Transport: Transport{Token: "s3cret"}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen != "Bearer s3cret" {
		t.Fatalf("Authorization %q", seen)
	}
}

func TestHostsFromURLs(t *testing.T) {
	got := HostsFromURLs([]string{"http://node-1:8000", "http://100.64.0.2:8082", "::bad"})
	if len(got) != 2 || got[0] != "node-1" || got[1] != "100.64.0.2" {
		t.Fatalf("%v", got)
	}
}
