package http

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// templateDir is the directory the server ships with.
const templateDir = "../html"

// The browser hashes exactly what it receives, so a template change the policy
// does not cover would silently disable the page.
func TestContentSecurityPolicyCoversPage(t *testing.T) {
	log.SetOutput(io.Discard)

	server := New(Config{TemplateDir: templateDir}, &testDb{}, NewCache(0))
	if server.template == nil {
		t.Fatal("expected the templates to load")
	}

	s := httptest.NewServer(server.Handler())
	defer s.Close()

	req, err := http.NewRequest(http.MethodGet, s.URL, nil)
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
	for _, block := range blocks {
		source := "'sha256-" + hashOf(block[2]) + "'"
		if !strings.Contains(policy, source) {
			t.Errorf("inline %s is not allowed by the policy", block[1])
		}
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	log.SetOutput(io.Discard)
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
	}
}

// A server without a page must still deny everything.
func TestPolicyWithoutTemplates(t *testing.T) {
	policy := contentSecurityPolicy(nil)
	for _, want := range []string{"script-src 'none'", "style-src 'none'", "default-src 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("expected %q in %s", want, policy)
		}
	}
}
