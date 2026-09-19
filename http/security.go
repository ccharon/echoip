package http

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
}

// inlineBlock matches the body of an inline script or style element.
var inlineBlock = regexp.MustCompile(`(?s)<(script|style)\b[^>]*>(.*?)</(?:script|style)>`)

// contentSecurityPolicy describes what the browser page may load. The hashes
// that allow its inline script and style cover the rendered page, because
// html/template drops comments from those elements.
func contentSecurityPolicy() string {
	var scripts, styles []string

	for _, block := range inlineBlocks() {
		source := "'sha256-" + hashOf(block.content) + "'"
		if block.tag == "script" {
			scripts = append(scripts, source)
		} else {
			styles = append(styles, source)
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
	if err := pageTemplate.ExecuteTemplate(&page, indexTemplate, &pageData{}); err != nil {
		return nil
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
