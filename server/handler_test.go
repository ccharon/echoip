package server

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ccharon/echoip/iputil/geo"
)

func lookupAddr(netip.Addr) (string, error) { return "localhost", nil }

type testDb struct{}

func (t *testDb) City(netip.Addr) (geo.City, error) {
	return geo.City{Name: "Bornyasherk", RegionName: "North Elbonia", RegionCode: "1234", PostalCode: "1234", Latitude: 63.416667, Longitude: 10.416667, Timezone: "Europe/Bornyasherk", CountryName: "Elbonia", CountryISO: "EB", CountryIsEU: new(bool)}, nil
}

func (t *testDb) ASN(netip.Addr) (geo.ASN, error) {
	return geo.ASN{AutonomousSystemNumber: 59795, AutonomousSystemOrganization: "Hosting4Real"}, nil
}

func (t *testDb) HasCity() bool { return true }
func (t *testDb) HasASN() bool  { return true }

func (t *testDb) CityBuilt() time.Time { return time.Date(2026, 9, 18, 4, 49, 38, 0, time.UTC) }
func (t *testDb) ASNBuilt() time.Time  { return time.Date(2026, 9, 19, 8, 15, 24, 0, time.UTC) }

// pendingDb stands in for databases that are downloaded after the server
// started.
type pendingDb struct {
	testDb
	city bool
	asn  bool
}

func (t *pendingDb) HasCity() bool { return t.city }
func (t *pendingDb) HasASN() bool  { return t.asn }

func testServer() *Server {
	cfg := Config{LookupAddr: lookupAddr}
	return New(cfg, &testDb{}, NewCache(100))
}

func httpGet(url string, acceptMediaType string, userAgent string) (string, int, error) {
	r, err := http.NewRequest("GET", url, nil)
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
	log.SetOutput(io.Discard)
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
		{s.URL + "/foo", "404 page not found", 404, "", ""},
		{s.URL + "/asn", "AS59795\n", 200, "", ""},
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
	log.SetOutput(io.Discard)
	s := httptest.NewServer(testServer().Handler())

	for _, path := range []string{"/health", "/", "/ip", "/json", "/country"} {
		r, err := http.NewRequest("HEAD", s.URL+path, nil)
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
	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(io.Discard)

	s := httptest.NewServer(testServer().Handler())
	if _, _, err := httpGet(s.URL+"/ip?ip=10.0.0.5", "", "curl/7.43.0"); err != nil {
		t.Fatal(err)
	}

	line := logged.String()
	for _, want := range []string{"127.0.0.1", "GET", "/ip?ip=10.0.0.5", "400", "not a public IP"} {
		if !strings.Contains(line, want) {
			t.Errorf("expected %q in the log line %q", want, line)
		}
	}
}

// The query value reaches the log through the error message, decoded, so a
// newline in it would forge a second line.
func TestLogLineCannotBeForged(t *testing.T) {
	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(io.Discard)

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
	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(io.Discard)

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
	log.SetOutput(io.Discard)
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
	if strings.Count(got, "9") > maxResizeBody {
		t.Errorf("the answer repeats the body: %q", got)
	}
}

// failingDB stands in for a database file that opened and then went bad.
type failingDB struct{ testDb }

func (*failingDB) City(netip.Addr) (geo.City, error) {
	return geo.City{}, errors.New("city database is unreadable")
}

func (*failingDB) ASN(netip.Addr) (geo.ASN, error) {
	return geo.ASN{}, errors.New("asn database is unreadable")
}

// A lookup that fails has to leave a trace, because an address the database
// does not hold returns no error at all.
func TestFailedLookupIsLogged(t *testing.T) {
	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(io.Discard)

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

	for _, want := range []string{"City lookup failed", "ASN lookup failed", "127.0.0.1"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("expected %q in the log %q", want, logged.String())
		}
	}
}

func TestPrivateIPParameter(t *testing.T) {
	log.SetOutput(io.Discard)
	s := httptest.NewServer(testServer().Handler())

	want := "{\n  \"status\": 400,\n  \"error\": \"not a public IP: 10.0.0.5\"\n}"
	for _, path := range []string{"/", "/ip", "/json", "/country", "/asn"} {
		out, status, err := httpGet(s.URL+path+"?ip=10.0.0.5", "", "curl/7.43.0")
		if err != nil {
			t.Fatal(err)
		}
		if status != 400 {
			t.Errorf("%s: expected 400, got %d", path, status)
		}
		if out != want {
			t.Errorf("%s: expected %q, got %q", path, want, out)
		}
	}

	// The browser page answers in plain text, since it renders no page for a
	// request it refuses.
	out, status, err := httpGet(s.URL+"?ip=10.0.0.5", "", "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}
	if status != 400 {
		t.Errorf("browser: expected 400, got %d", status)
	}
	if want := "not a public IP: 10.0.0.5"; out != want {
		t.Errorf("browser: expected %q, got %q", want, out)
	}
}

func TestDisabledHandlers(t *testing.T) {
	log.SetOutput(io.Discard)
	server := testServer()
	server.cfg.LookupAddr = nil
	server.geo, _ = geo.Open("", "")
	s := httptest.NewServer(server.Handler())

	tests := []struct {
		url    string
		out    string
		status int
	}{
		{s.URL + "/country", "404 page not found", 404},
		{s.URL + "/country-iso", "404 page not found", 404},
		{s.URL + "/city", "404 page not found", 404},
		{s.URL + "/coordinates", "404 page not found", 404},
		{s.URL + "/asn", "404 page not found", 404},
		{s.URL + "/json", "{\n  \"ip\": \"127.0.0.1\",\n  \"ip_decimal\": 2130706433\n}", 200},
	}

	for _, tt := range tests {
		out, status, err := httpGet(tt.url, "", "")
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

func TestJSONHandlers(t *testing.T) {
	log.SetOutput(io.Discard)
	s := httptest.NewServer(testServer().Handler())

	tests := []struct {
		url    string
		out    string
		status int
	}{
		{s.URL, "{\n  \"ip\": \"127.0.0.1\",\n  \"ip_decimal\": 2130706433,\n  \"country\": \"Elbonia\",\n  \"country_iso\": \"EB\",\n  \"country_eu\": false,\n  \"region_name\": \"North Elbonia\",\n  \"region_code\": \"1234\",\n  \"zip_code\": \"1234\",\n  \"city\": \"Bornyasherk\",\n  \"latitude\": 63.416667,\n  \"longitude\": 10.416667,\n  \"time_zone\": \"Europe/Bornyasherk\",\n  \"asn\": \"AS59795\",\n  \"asn_org\": \"Hosting4Real\",\n  \"hostname\": \"localhost\",\n  \"user_agent\": {\n    \"product\": \"curl\",\n    \"version\": \"7.2.6.0\",\n    \"raw_value\": \"curl/7.2.6.0\"\n  }\n}", 200},
		{s.URL + "/port/1337", "{\n  \"status\": 404,\n  \"error\": \"404 page not found\"\n}", 404},
		{s.URL + "/foo", "{\n  \"status\": 404,\n  \"error\": \"404 page not found\"\n}", 404},
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
	log.SetOutput(io.Discard)
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
	log.SetOutput(io.Discard)
	srv := testServer()
	srv.cfg.Profile = true
	s := httptest.NewServer(srv.Handler())
	_, got, err := httpPost(s.URL+"/debug/cache/resize", "10")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"message\": \"Changed cache capacity to 10.\"\n}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := srv.cache.Stats().Capacity; got != 10 {
		t.Errorf("capacity = %d, want 10", got)
	}
}

func TestGeoHandlersFollowDatabase(t *testing.T) {
	db := &pendingDb{}
	server := testServer()
	server.geo = db
	s := httptest.NewServer(server.Handler())
	defer s.Close()

	status := func(url string) int {
		t.Helper()
		_, status, err := httpGet(s.URL+url, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return status
	}

	if got := status("/country"); got != 404 {
		t.Errorf("Expected 404 for /country while the database is missing, got %d", got)
	}
	if got := status("/asn"); got != 404 {
		t.Errorf("Expected 404 for /asn while the database is missing, got %d", got)
	}

	// Each database enables its own routes.
	db.asn = true
	if got := status("/country"); got != 404 {
		t.Errorf("Expected 404 for /country with only the ASN database, got %d", got)
	}
	if got := status("/asn"); got != 200 {
		t.Errorf("Expected 200 for /asn with the ASN database loaded, got %d", got)
	}

	db.city = true
	out, got, err := httpGet(s.URL+"/country", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != 200 {
		t.Fatalf("Expected 200 after the city database loaded, got %d", got)
	}
	if want := "Elbonia\n"; out != want {
		t.Errorf("Expected %q, got %q", want, out)
	}
}

// absentCityDb stands in for a server that holds only the ASN database.
type absentCityDb struct{ testDb }

func (t *absentCityDb) CityBuilt() time.Time { return time.Time{} }

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
	server.geo = &absentCityDb{}
	if got, want := server.geoBuilt(), "ASN database built 2026-09-19"; got != want {
		t.Errorf("Expected %q, got %q", want, got)
	}
}
