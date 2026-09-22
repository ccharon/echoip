package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The browser hashes exactly what it receives, so a template change the policy
// does not cover would silently disable the page. The error page is checked
// the same way.
func TestContentSecurityPolicyCoversPage(t *testing.T) {
	server := New(Config{}, &testDB{}, NewCache(0))

	s := httptest.NewServer(server.Handler())
	defer s.Close()

	for _, path := range []string{"/", "/nowhere"} {
		t.Run(path, func(t *testing.T) {
			checkPolicyCovers(t, s.URL+path)
		})
	}
}

func checkPolicyCovers(t *testing.T, url string) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	policy := res.Header.Get("Content-Security-Policy")
	if policy == "" {
		t.Fatal("expected a Content-Security-Policy header")
	}
	if strings.Contains(policy, "unsafe-inline") || strings.Contains(policy, "unsafe-eval") {
		t.Errorf("policy relaxes inline execution: %s", policy)
	}

	blocks := inlineBlock.FindAllStringSubmatch(string(body), -1)
	if len(blocks) == 0 {
		t.Fatal("expected the page to hold inline blocks")
	}
	// The hash has to sit in the directive that governs its element. A hash
	// under the wrong one leaves both blocked.
	directives := map[string]string{}
	for _, part := range strings.Split(policy, "; ") {
		name, sources, _ := strings.Cut(part, " ")
		directives[name] = sources
	}

	for _, block := range blocks {
		source := "'sha256-" + hashOf(block[2]) + "'"
		directive := block[1] + "-src"
		if !strings.Contains(directives[directive], source) {
			t.Errorf("inline %s is not allowed by %s: %s", block[1], directive, directives[directive])
		}
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())
	defer s.Close()

	for _, path := range []string{"/", "/ip", "/json", "/health", "/nowhere"} {
		res, err := http.Get(s.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()

		for name, want := range securityHeaders {
			if got := res.Header.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", path, name, got, want)
			}
		}
		if res.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: missing Content-Security-Policy", path)
		}
		if got := res.Header.Get("Vary"); got != "Accept, User-Agent" {
			t.Errorf("%s: Vary = %q, want %q", path, got, "Accept, User-Agent")
		}
	}
}

// An empty hash list denies the source rather than allowing everything.
func TestSourcesWithoutHashes(t *testing.T) {
	if got := sources(nil); got != "'none'" {
		t.Errorf("sources(nil) = %q, want %q", got, "'none'")
	}
}
