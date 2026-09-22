package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ccharon/echoip/iputil/geo"
)

func lookupAddr(context.Context, netip.Addr) (string, error) { return "localhost", nil }

type testDB struct{}

func (t *testDB) City(netip.Addr) (geo.City, error) {
	return geo.City{Name: "Bornyasherk", RegionName: "North Elbonia", RegionCode: "1234", PostalCode: "1234", Latitude: new(63.416667), Longitude: new(10.416667), Timezone: "Europe/Bornyasherk", CountryName: "Elbonia", CountryISO: "EB", CountryIsEU: new(bool)}, nil
}

func (t *testDB) ASN(netip.Addr) (geo.ASN, error) {
	return geo.ASN{AutonomousSystemNumber: 59795, AutonomousSystemOrganization: "Hosting4Real"}, nil
}

func (t *testDB) HasCity() bool { return true }
func (t *testDB) HasASN() bool  { return true }

func (t *testDB) CityBuilt() time.Time { return time.Date(2026, 9, 18, 4, 49, 38, 0, time.UTC) }
func (t *testDB) ASNBuilt() time.Time  { return time.Date(2026, 9, 19, 8, 15, 24, 0, time.UTC) }

// pendingDB stands in for databases that are downloaded after the server
// started.
type pendingDB struct {
	testDB
	city bool
	asn  bool
}

func (t *pendingDB) HasCity() bool { return t.city }
func (t *pendingDB) HasASN() bool  { return t.asn }

const notFoundJSON = "{\n  \"type\": \"https://github.com/ccharon/echoip#not-found\",\n  \"title\": \"Not found\",\n  \"status\": 404,\n  \"detail\": \"No endpoint answers at this path.\"\n}"

func testServer() *Server {
	cfg := Config{LookupAddr: lookupAddr, City: true, ASN: true}
	return New(cfg, &testDB{}, NewCache(100))
}

func httpGet(url string, acceptMediaType string, userAgent string) (string, int, error) {
	r, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	if acceptMediaType != "" {
		r.Header.Set("Accept", acceptMediaType)
	}
	r.Header.Set("User-Agent", userAgent)

	res, err := http.DefaultClient.Do(r)
	if err != nil {
		return "", 0, err
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", 0, err
	}

	return string(data), res.StatusCode, nil
}

func httpPost(url, body string) (*http.Response, string, error) {
	r, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}

	res, err := http.DefaultClient.Do(r)
	if err != nil {
		return nil, "", err
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}

	return res, string(data), nil
}

func TestCLIHandlers(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())

	tests := []struct {
		url             string
		out             string
		status          int
		userAgent       string
		acceptMediaType string
	}{
		{s.URL, "127.0.0.1\n", 200, "curl/7.43.0", ""},
		{s.URL, "127.0.0.1\n", 200, "foo/bar", textMediaType},
		{s.URL + "/ip", "127.0.0.1\n", 200, "", ""},
		{s.URL + "/country", "Elbonia\n", 200, "", ""},
		{s.URL + "/country-iso", "EB\n", 200, "", ""},
		{s.URL + "/coordinates", "63.416667,10.416667\n", 200, "", ""},
		{s.URL + "/city", "Bornyasherk\n", 200, "", ""},
		{s.URL + "/foo", "error: No endpoint answers at this path.\n", 404, "curl/7.43.0", ""},
		{s.URL + "/asn", "AS59795\n", 200, "", ""},
		{s.URL + "/asn-org", "Hosting4Real\n", 200, "", ""},
	}

	for _, tt := range tests {
		out, status, err := httpGet(tt.url, tt.acceptMediaType, tt.userAgent)
		if err != nil {
			t.Fatal(err)
		}
		if status != tt.status {
			t.Errorf("Expected %d, got %d", tt.status, status)
		}
		if out != tt.out {
			t.Errorf("Expected %q, got %q", tt.out, out)
		}
	}
}

// HEAD has to answer like GET, because monitoring uses it on /health.
func TestHeadRequests(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())

	for _, path := range []string{"/health", "/", "/ip", "/json", "/country"} {
		r, err := http.NewRequest(http.MethodHead, s.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("User-Agent", "curl/7.43.0")

		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()

		if res.StatusCode != 200 {
			t.Errorf("HEAD %s: expected 200, got %d", path, res.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("HEAD %s: expected no body, got %q", path, body)
		}
	}

	// A path that does not exist stays a 404 for HEAD as well.
	res, err := http.DefaultClient.Head(s.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("HEAD /nope: expected 404, got %d", res.StatusCode)
	}
}

// A refused request is the only trace a probe leaves, so it has to reach the
// log with the peer address, the target and the reason.
func TestRefusedRequestIsLogged(t *testing.T) {
	logged := captureLog(t)

	s := httptest.NewServer(testServer().Handler())
	if _, _, err := httpGet(s.URL+"/ip?ip=10.0.0.5", "", "curl/7.43.0"); err != nil {
		t.Fatal(err)
	}

	line := logged.String()
	for _, want := range []string{"127.0.0.1", "GET", "/ip?ip=10.0.0.5", "400", "not-public-address"} {
		if !strings.Contains(line, want) {
			t.Errorf("expected %q in the log line %q", want, line)
		}
	}
}

// Behind a proxy the peer is always the proxy, so the log needs the address
// the proxy reported to name the client. An untrusted peer's header is left out.
func TestRefusedRequestNamesForwardedClient(t *testing.T) {
	tests := []struct {
		trusted string
		logged  bool
	}{
		{"127.0.0.1/32", true},
		{"192.0.2.0/24", false},
	}

	for _, tt := range tests {
		logged := captureLog(t)

		cfg := Config{IPHeaders: []string{"X-Real-IP"}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix(tt.trusted)}}
		s := httptest.NewServer(New(cfg, &testDB{}, NewCache(0)).Handler())

		r, err := http.NewRequest(http.MethodGet, s.URL+"/nowhere", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Real-IP", "198.51.100.7")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		s.Close()

		line := logged.String()
		if got := strings.Contains(line, "client=198.51.100.7"); got != tt.logged {
			t.Errorf("trusting %s: forwarded address logged = %v, want %v in %q", tt.trusted, got, tt.logged, line)
		}
		if !strings.Contains(line, "127.0.0.1") {
			t.Errorf("trusting %s: expected the peer address in %q", tt.trusted, line)
		}
	}
}

// The query value reaches the log through the error message, decoded, so a
// newline in it would forge a second line.
func TestLogLineCannotBeForged(t *testing.T) {
	logged := captureLog(t)

	s := httptest.NewServer(testServer().Handler())
	forged := "%0a2026/01/01%2000:00:00%20echoip:%20forged"
	if _, _, err := httpGet(s.URL+"/ip?ip="+forged, "", "curl/7.43.0"); err != nil {
		t.Fatal(err)
	}

	out := logged.String()
	if lines := strings.Count(strings.TrimSuffix(out, "\n"), "\n"); lines != 0 {
		t.Errorf("expected one line, got %d extra: %q", lines, out)
	}
	if !strings.Contains(out, `\n2026/01/01`) {
		t.Errorf("expected the newline to be escaped in %q", out)
	}
}

// One request must not be able to write an arbitrarily long line.
func TestLogLineIsBounded(t *testing.T) {
	logged := captureLog(t)

	s := httptest.NewServer(testServer().Handler())
	if _, _, err := httpGet(s.URL+"/ip?ip="+strings.Repeat("A", 4000), "", "curl/7.43.0"); err != nil {
		t.Fatal(err)
	}

	if out := logged.String(); len(out) > 512 {
		t.Errorf("expected a bounded line, got %d bytes", len(out))
	} else if !strings.Contains(out, "...") {
		t.Errorf("expected the value to be clipped in %q", out)
	}
}

// A body that is not a number must not decide how much the server reads or how
// much it sends back.
func TestCacheResizeBoundsTheBody(t *testing.T) {
	srv := testServer()
	srv.cfg.Profile = true
	s := httptest.NewServer(srv.Handler())

	res, got, err := httpPost(s.URL+"/debug/cache/resize", strings.Repeat("9", 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
	if len(got) > 512 {
		t.Errorf("expected a bounded answer, got %d bytes", len(got))
	}
	var problem struct{ Detail string }
	if err := json.Unmarshal([]byte(got), &problem); err != nil {
		t.Fatal(err)
	}
	if strings.Count(problem.Detail, "9") > maxResizeBody {
		t.Errorf("the answer repeats the body: %q", got)
	}
}

// failingDB stands in for a database file that opened and then went bad.
type failingDB struct{ testDB }

func (*failingDB) City(netip.Addr) (geo.City, error) {
	return geo.City{}, errors.New("city database is unreadable")
}

func (*failingDB) ASN(netip.Addr) (geo.ASN, error) {
	return geo.ASN{}, errors.New("asn database is unreadable")
}

// A lookup that fails has to leave a trace, because an address the database
// does not hold returns no error at all.
func TestFailedLookupIsLogged(t *testing.T) {
	logged := captureLog(t)

	server := New(Config{}, &failingDB{}, NewCache(0))
	s := httptest.NewServer(server.Handler())

	out, status, err := httpGet(s.URL+"/json", "", "curl/7.43.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Errorf("expected the response without geo data, got %d", status)
	}
	if !strings.Contains(out, `"ip": "127.0.0.1"`) {
		t.Errorf("expected the address in %q", out)
	}

	for _, want := range []string{"city lookup failed", "ASN lookup failed", "127.0.0.1"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("expected %q in the log %q", want, logged.String())
		}
	}
}

// A refused address comes in the format a success on the same path would
// have come in.
func TestPrivateIPParameter(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())
	defer s.Close()

	text := "error: 10.0.0.5 is not a public address and is not looked up.\n"
	problem := "{\n  \"type\": \"https://github.com/ccharon/echoip#not-public-address\",\n  \"title\": \"Address is not public\",\n  \"status\": 400,\n  \"detail\": \"10.0.0.5 is not a public address and is not looked up.\"\n}"
	tests := []struct {
		path      string
		userAgent string
		want      string
	}{
		{"/", "curl/7.43.0", text},
		{"/ip", "curl/7.43.0", text},
		{"/country", "curl/7.43.0", text},
		{"/asn", "Mozilla/5.0", text},
		{"/json", "curl/7.43.0", problem},
		{"/json", "Mozilla/5.0", problem},
	}

	for _, tt := range tests {
		out, status, err := httpGet(s.URL+tt.path+"?ip=10.0.0.5", "", tt.userAgent)
		if err != nil {
			t.Fatal(err)
		}
		if status != 400 {
			t.Errorf("%s: expected 400, got %d", tt.path, status)
		}
		if out != tt.want {
			t.Errorf("%s as %s: expected %q, got %q", tt.path, tt.userAgent, tt.want, out)
		}
	}

	// The browser gets the error page.
	out, status, err := httpGet(s.URL+"?ip=10.0.0.5", "", "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 400 {
		t.Errorf("browser: expected 400, got %d", status)
	}
	for _, want := range []string{"Error 400", "Address is not public", "10.0.0.5 is not a public address"} {
		if !strings.Contains(out, want) {
			t.Errorf("browser: expected %q in the error page", want)
		}
	}
}

// A database that is not configured leaves its endpoints out entirely.
func TestDisabledHandlers(t *testing.T) {
	server := testServer()
	server.cfg.LookupAddr = nil
	server.cfg.City = false
	server.cfg.ASN = false
	server.geo, _ = geo.Open("", "")
	s := httptest.NewServer(server.Handler())
	defer s.Close()

	for _, path := range []string{"/country", "/country-iso", "/city", "/coordinates", "/asn", "/asn-org"} {
		out, status, err := httpGet(s.URL+path, "", "curl/7.43.0")
		if err != nil {
			t.Fatal(err)
		}
		if status != 404 {
			t.Errorf("%s: expected 404, got %d", path, status)
		}
		if want := "error: No endpoint answers at this path.\n"; out != want {
			t.Errorf("%s: expected %q, got %q", path, want, out)
		}
	}

	out, status, err := httpGet(s.URL+"/json", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"ip\": \"127.0.0.1\"\n}"; status != 200 || out != want {
		t.Errorf("/json: expected 200 and %q, got %d and %q", want, status, out)
	}
}

func TestJSONHandlers(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())

	tests := []struct {
		url    string
		out    string
		status int
	}{
		{s.URL, "{\n  \"ip\": \"127.0.0.1\",\n  \"country\": \"Elbonia\",\n  \"country_iso\": \"EB\",\n  \"country_eu\": false,\n  \"region_name\": \"North Elbonia\",\n  \"region_code\": \"1234\",\n  \"zip_code\": \"1234\",\n  \"city\": \"Bornyasherk\",\n  \"latitude\": 63.416667,\n  \"longitude\": 10.416667,\n  \"time_zone\": \"Europe/Bornyasherk\",\n  \"asn\": \"AS59795\",\n  \"asn_org\": \"Hosting4Real\",\n  \"hostname\": \"localhost\",\n  \"user_agent\": {\n    \"product\": \"curl\",\n    \"version\": \"7.2.6.0\",\n    \"raw_value\": \"curl/7.2.6.0\"\n  }\n}", 200},
		{s.URL + "/port/1337", notFoundJSON, 404},
		{s.URL + "/foo", notFoundJSON, 404},
		{s.URL + "/health", `{"status":"OK"}`, 200},
	}

	for _, tt := range tests {
		out, status, err := httpGet(tt.url, jsonMediaType, "curl/7.2.6.0")
		if err != nil {
			t.Fatal(err)
		}
		if status != tt.status {
			t.Errorf("Expected %d for %s, got %d", tt.status, tt.url, status)
		}
		if out != tt.out {
			t.Errorf("Expected %q for %s, got %q", tt.out, tt.url, out)
		}
	}
}

func TestCacheHandler(t *testing.T) {
	srv := testServer()
	srv.cfg.Profile = true
	s := httptest.NewServer(srv.Handler())
	got, _, err := httpGet(s.URL+"/debug/cache/", jsonMediaType, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"size\": 0,\n  \"capacity\": 100,\n  \"evictions\": 0\n}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCacheResizeHandler(t *testing.T) {
	srv := testServer()
	srv.cfg.Profile = true
	s := httptest.NewServer(srv.Handler())
	_, got, err := httpPost(s.URL+"/debug/cache/resize", "10\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"message\": \"Changed cache capacity to 10.\"\n}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := srv.cache.stats().Capacity; got != 10 {
		t.Errorf("capacity = %d, want 10", got)
	}
}

// A configured database that is still downloading answers 503 with the time
// the updater tries again.
func TestGeoHandlersFollowDatabase(t *testing.T) {
	db := &pendingDB{}
	server := testServer()
	server.geo = db
	s := httptest.NewServer(server.Handler())
	defer s.Close()

	get := func(path string) (*http.Response, string) {
		t.Helper()
		r, err := http.NewRequest(http.MethodGet, s.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("User-Agent", "curl/7.43.0")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, string(body)
	}

	res, out := get("/country")
	if res.StatusCode != 503 {
		t.Errorf("Expected 503 for /country while the database is missing, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Retry-After"); got != retryAfter {
		t.Errorf("Expected Retry-After %s, got %q", retryAfter, got)
	}
	if want := "error: The city database is not loaded yet.\n"; out != want {
		t.Errorf("Expected %q, got %q", want, out)
	}
	if res, _ := get("/asn"); res.StatusCode != 503 {
		t.Errorf("Expected 503 for /asn while the database is missing, got %d", res.StatusCode)
	}

	// Each database enables its own routes.
	db.asn = true
	if res, _ := get("/country"); res.StatusCode != 503 {
		t.Errorf("Expected 503 for /country with only the ASN database, got %d", res.StatusCode)
	}
	if res, _ := get("/asn"); res.StatusCode != 200 {
		t.Errorf("Expected 200 for /asn with the ASN database loaded, got %d", res.StatusCode)
	}

	db.city = true
	res, out = get("/country")
	if res.StatusCode != 200 {
		t.Fatalf("Expected 200 after the city database loaded, got %d", res.StatusCode)
	}
	if want := "Elbonia\n"; out != want {
		t.Errorf("Expected %q, got %q", want, out)
	}
}

// absentCityDB stands in for a server that holds only the ASN database.
type absentCityDB struct{ testDB }

func (t *absentCityDB) CityBuilt() time.Time { return time.Time{} }

func TestPageNamesTheDatabaseBuilds(t *testing.T) {
	server := testServer()
	s := httptest.NewServer(server.Handler())
	defer s.Close()

	out, status, err := httpGet(s.URL, "", "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("Expected 200, got %d", status)
	}
	if want := "City database built 2026-09-18, ASN database built 2026-09-19."; !strings.Contains(out, want) {
		t.Errorf("Expected the footer to name %q", want)
	}

	// A database that is not loaded is left out rather than dated to the zero
	// time.
	server.geo = &absentCityDB{}
	if got, want := server.geoBuilt(), "ASN database built 2026-09-19"; got != want {
		t.Errorf("Expected %q, got %q", want, got)
	}
}

func TestClipBoundary(t *testing.T) {
	exact := strings.Repeat("a", maxValueLen)
	if got := clip(exact); got != exact {
		t.Errorf("a value of exactly %d characters was clipped: %q", maxValueLen, got)
	}

	over := exact + "b"
	if want := exact + "..."; clip(over) != want {
		t.Errorf("Expected %q, got %q", want, clip(over))
	}
}

// Every failure carries the content type of the format it is written in.
func TestErrorContentType(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())
	defer s.Close()

	tests := []struct {
		path      string
		accept    string
		userAgent string
		want      string
	}{
		{"/ip?ip=10.0.0.5", "", "curl/7.43.0", textContentType},
		{"/json?ip=10.0.0.5", "", "curl/7.43.0", problemMediaType},
		{"/?ip=10.0.0.5", "", "Mozilla/5.0", htmlContentType},
		{"/?ip=10.0.0.5", jsonMediaType, "Mozilla/5.0", problemMediaType},
		{"/nowhere", "", "curl/7.43.0", textContentType},
		{"/nowhere", "", "Mozilla/5.0", htmlContentType},
		{"/nowhere", jsonMediaType, "Mozilla/5.0", problemMediaType},
	}

	for _, tt := range tests {
		r, err := http.NewRequest(http.MethodGet, s.URL+tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tt.accept != "" {
			r.Header.Set("Accept", tt.accept)
		}
		r.Header.Set("User-Agent", tt.userAgent)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if got := res.Header.Get("Content-Type"); got != tt.want {
			t.Errorf("%s as %s with Accept %q: Content-Type %q, want %q", tt.path, tt.userAgent, tt.accept, got, tt.want)
		}
	}
}

// A body that is not a capacity is named as such, while the parse error stays
// in the log.
func TestInvalidCapacity(t *testing.T) {
	srv := testServer()
	srv.cfg.Profile = true
	s := httptest.NewServer(srv.Handler())
	defer s.Close()

	for _, body := range []string{"many", "-1", "5abc", "5 6", "5" + strings.Repeat(" ", maxResizeBody)} {
		res, got, err := httpPost(s.URL+"/debug/cache/resize", body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 400 {
			t.Errorf("%q: expected 400, got %d", body, res.StatusCode)
		}
		var problem struct{ Type, Detail string }
		if err := json.Unmarshal([]byte(got), &problem); err != nil {
			t.Fatal(err)
		}
		if want := problemBase + invalidCapacity.slug; problem.Type != want {
			t.Errorf("%q: type %q, want %q", body, problem.Type, want)
		}
		if strings.Contains(problem.Detail, "strconv") || strings.Contains(problem.Detail, "expected") {
			t.Errorf("%q: detail carries the parse error: %q", body, problem.Detail)
		}
	}
}

// A path that exists answers other methods with 405 and names the ones it
// takes, while an unknown path stays a 404.
func TestMethodNotAllowed(t *testing.T) {
	srv := testServer()
	srv.cfg.Profile = true
	s := httptest.NewServer(srv.Handler())
	defer s.Close()

	// The client is Go-http-client, so a negotiated answer is text.
	tests := []struct {
		method      string
		path        string
		status      int
		allow       string
		contentType string
	}{
		{http.MethodPost, "/", 405, "GET, HEAD, OPTIONS", textContentType},
		{http.MethodDelete, "/json", 405, "GET, HEAD, OPTIONS", problemMediaType},
		{http.MethodPost, "/ip", 405, "GET, HEAD, OPTIONS", textContentType},
		{http.MethodGet, "/debug/cache/resize", 405, "POST, OPTIONS", problemMediaType},
		{http.MethodOptions, "/ip", 204, "GET, HEAD, OPTIONS", ""},
		{http.MethodPost, "/nowhere", 404, "", textContentType},
		{http.MethodOptions, "/nowhere", 404, "", textContentType},
	}

	for _, tt := range tests {
		r, err := http.NewRequest(tt.method, s.URL+tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()

		if res.StatusCode != tt.status {
			t.Errorf("%s %s: status %d, want %d", tt.method, tt.path, res.StatusCode, tt.status)
		}
		if got := res.Header.Get("Allow"); got != tt.allow {
			t.Errorf("%s %s: Allow %q, want %q", tt.method, tt.path, got, tt.allow)
		}
		if got := res.Header.Get("Content-Type"); got != tt.contentType {
			t.Errorf("%s %s: Content-Type %q, want %q", tt.method, tt.path, got, tt.contentType)
		}
	}
}

// A 405 comes in the format of the route, even for a browser that would get
// the page on /.
func TestMethodNotAllowedTakesRouteFormat(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())
	defer s.Close()

	r, err := http.NewRequest(http.MethodPost, s.URL+"/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got := res.Header.Get("Content-Type"); got != problemMediaType {
		t.Errorf("Content-Type %q, want %q", got, problemMediaType)
	}
}

// The text endpoints name their type, which HEAD cannot get from sniffing a
// body it never writes.
func TestTextContentType(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())
	defer s.Close()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/ip", "/country", "/coordinates"} {
			r, err := http.NewRequest(method, s.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if got := res.Header.Get("Content-Type"); got != textContentType {
				t.Errorf("%s %s: Content-Type %q, want %q", method, path, got, textContentType)
			}
		}
	}
}

// / answers in the type the Accept header weights highest.
func TestNegotiatedRoot(t *testing.T) {
	s := httptest.NewServer(testServer().Handler())
	defer s.Close()

	tests := []struct {
		accept string
		want   string
	}{
		{"application/json", jsonMediaType},
		{"text/html, application/json;q=0.5", htmlContentType},
		{"text/html;q=0.5, application/json", jsonMediaType},
		{"text/plain;q=0.9, application/json;q=0.8", textContentType},
	}

	for _, tt := range tests {
		r, err := http.NewRequest(http.MethodGet, s.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Accept", tt.accept)
		r.Header.Set("User-Agent", "Mozilla/5.0")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if got := res.Header.Get("Content-Type"); got != tt.want {
			t.Errorf("Accept %q: Content-Type %q, want %q", tt.accept, got, tt.want)
		}
	}
}

// A panic before the handler writes becomes a 500 in the format of the
// request, with the stack in the log.
func TestPanicAnswersInternalError(t *testing.T) {
	logged := captureLog(t)
	h := recoverPanic(func(http.ResponseWriter, *http.Request) *appError {
		panic("boom")
	})
	s := httptest.NewServer(h)
	defer s.Close()

	out, status, err := httpGet(s.URL, "", "curl/7.43.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 500 {
		t.Errorf("expected 500, got %d", status)
	}
	if want := "error: The request could not be completed.\n"; out != want {
		t.Errorf("expected %q, got %q", want, out)
	}
	if !strings.Contains(logged.String(), "handler panicked") || !strings.Contains(logged.String(), "boom") {
		t.Errorf("expected the panic in the log, got %q", logged.String())
	}
}

// A panic after the handler wrote drops the connection, so the client cannot
// take the half answer for a whole one.
func TestPanicAfterWriteDropsConnection(t *testing.T) {
	captureLog(t)
	h := recoverPanic(func(w http.ResponseWriter, _ *http.Request) *appError {
		writeRaw(w, "partial")
		panic("boom")
	})
	s := httptest.NewServer(h)
	defer s.Close()

	res, err := http.Get(s.URL)
	if err != nil {
		return
	}
	defer func() { _ = res.Body.Close() }()
	if _, err := io.ReadAll(res.Body); err == nil {
		t.Error("expected the connection to be dropped")
	}
}

// The page offers the field endpoints the server registered, and names the
// database a registered one is waiting for.
func TestPageOffersRegisteredEndpoints(t *testing.T) {
	server := testServer()
	server.cfg.ASN = false
	server.geo = &pendingDB{}
	s := httptest.NewServer(server.Handler())
	defer s.Close()

	out, status, err := httpGet(s.URL, "", "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	for _, ep := range cityEndpoints {
		button := regexp.MustCompile(`data-path="` + ep.path + `" data-value="[^"]*" data-unavailable="The city database is not loaded yet."`)
		if !button.MatchString(out) {
			t.Errorf("expected a button for /%s that names the missing database", ep.path)
		}
	}
	for _, ep := range asnEndpoints {
		if strings.Contains(out, `data-path="`+ep.path+`"`) {
			t.Errorf("expected no button for /%s, which is not registered", ep.path)
		}
	}
}
