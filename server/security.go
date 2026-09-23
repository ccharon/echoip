package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

// Headers sent with every response.
var securityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"Referrer-Policy":        "no-referrer",
	"X-Frame-Options":        "DENY",
	// Every answer names the address of whoever asked, so no cache between
	// the service and the client may hand it to someone else.
	"Cache-Control": "no-store",
}

// inlineBlock matches the body of an inline script or style element.
var inlineBlock = regexp.MustCompile(`(?s)<(script|style)\b[^>]*>(.*?)</(?:script|style)>`)

// contentSecurityPolicy describes what the browser pages may load. The hashes
// that allow their inline script and style cover the rendered pages, because
// html/template drops comments from those elements.
func contentSecurityPolicy() string {
	var scripts, styles []string

	for _, block := range inlineBlocks() {
		source := "'sha256-" + hashOf(block.content) + "'"
		if block.tag == "script" {
			scripts = appendOnce(scripts, source)
		} else {
			styles = appendOnce(styles, source)
		}
	}

	return strings.Join([]string{
		"default-src 'none'",
		"script-src " + sources(scripts),
		"style-src " + sources(styles),
		// The map is the only third party the page reaches out to.
		"frame-src https://www.openstreetmap.org",
		"img-src 'self' data:",
		"connect-src 'self'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

type inlineContent struct {
	tag     string
	content string
}

func inlineBlocks() []inlineContent {
	var page bytes.Buffer
	// A page that cannot be rendered would leave the policy without hashes,
	// which serves a page the browser refuses to run.
	if err := pageTemplate.ExecuteTemplate(&page, indexTemplate, &pageData{}); err != nil {
		panic("rendering the page for the content security policy: " + err.Error())
	}
	if err := pageTemplate.ExecuteTemplate(&page, errorTemplate, &errorData{}); err != nil {
		panic("rendering the error page for the content security policy: " + err.Error())
	}

	var blocks []inlineContent
	for _, match := range inlineBlock.FindAllStringSubmatch(page.String(), -1) {
		blocks = append(blocks, inlineContent{tag: match[1], content: match[2]})
	}
	return blocks
}

func sources(hashes []string) string {
	if len(hashes) == 0 {
		return "'none'"
	}
	return strings.Join(hashes, " ")
}

func hashOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return base64.StdEncoding.EncodeToString(sum[:])
}
