package http

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/ccharon/echoip/iputil/geo"
)

func lookupAddr(netip.Addr) (string, error) { return "localhost", nil }
func lookupPort(netip.Addr, uint16) error   { return nil }

type testDb struct{}

func (t *testDb) City(netip.Addr) (geo.City, error) {
	return geo.City{Name: "Bornyasherk", RegionName: "North Elbonia", RegionCode: "1234", MetroCode: 1234, PostalCode: "1234", Latitude: 63.416667, Longitude: 10.416667, Timezone: "Europe/Bornyasherk", CountryName: "Elbonia", CountryISO: "EB", CountryIsEU: new(bool)}, nil
}

func (t *testDb) ASN(netip.Addr) (geo.ASN, error) {
	return geo.ASN{AutonomousSystemNumber: 59795, AutonomousSystemOrganization: "Hosting4Real"}, nil
}

func (t *testDb) HasCity() bool { return true }
func (t *testDb) HasASN() bool  { return true }

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
	cfg := Config{LookupAddr: lookupAddr, LookupPort: lookupPort}
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

func TestDisabledHandlers(t *testing.T) {
	log.SetOutput(io.Discard)
	server := testServer()
	server.cfg.LookupPort = nil
	server.cfg.LookupAddr = nil
	server.geo, _ = geo.Open("", "")
	s := httptest.NewServer(server.Handler())

	tests := []struct {
		url    string
		out    string
		status int
	}{
		{s.URL + "/port/1337", "404 page not found", 404},
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
		{s.URL, "{\n  \"ip\": \"127.0.0.1\",\n  \"ip_decimal\": 2130706433,\n  \"country\": \"Elbonia\",\n  \"country_iso\": \"EB\",\n  \"country_eu\": false,\n  \"region_name\": \"North Elbonia\",\n  \"region_code\": \"1234\",\n  \"metro_code\": 1234,\n  \"zip_code\": \"1234\",\n  \"city\": \"Bornyasherk\",\n  \"latitude\": 63.416667,\n  \"longitude\": 10.416667,\n  \"time_zone\": \"Europe/Bornyasherk\",\n  \"asn\": \"AS59795\",\n  \"asn_org\": \"Hosting4Real\",\n  \"hostname\": \"localhost\",\n  \"user_agent\": {\n    \"product\": \"curl\",\n    \"version\": \"7.2.6.0\",\n    \"raw_value\": \"curl/7.2.6.0\"\n  }\n}", 200},
		{s.URL + "/port/foo", "{\n  \"status\": 400,\n  \"error\": \"invalid port: foo\"\n}", 400},
		{s.URL + "/port/0", "{\n  \"status\": 400,\n  \"error\": \"invalid port: 0\"\n}", 400},
		{s.URL + "/port/65537", "{\n  \"status\": 400,\n  \"error\": \"invalid port: 65537\"\n}", 400},
		{s.URL + "/port/31337", "{\n  \"status\": 400,\n  \"error\": \"cannot check 127.0.0.1\"\n}", 400}, // the caller is not a routable target
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

// portServer trusts X-Real-IP, so a test can present a routable address.
func portServer() *Server {
	server := testServer()
	server.cfg.IPHeaders = []string{"X-Real-IP"}
	return server
}

func TestPortHandler(t *testing.T) {
	log.SetOutput(io.Discard)
	s := httptest.NewServer(portServer().Handler())
	defer s.Close()

	tests := []struct {
		url    string
		header string
		out    string
		status int
	}{
		{s.URL + "/port/31337", "1.3.3.7", "{\n  \"ip\": \"1.3.3.7\",\n  \"port\": 31337,\n  \"reachable\": true\n}", 200},
		// The ip parameter must not aim the check at another host.
		{s.URL + "/port/80?ip=9.9.9.9", "1.3.3.7", "{\n  \"ip\": \"1.3.3.7\",\n  \"port\": 80,\n  \"reachable\": true\n}", 200},
		// Neither must the trusted header, which a caller controls whenever it
		// reaches the server without a proxy in front of it.
		{s.URL + "/port/22", "127.0.0.1", "{\n  \"status\": 400,\n  \"error\": \"cannot check 127.0.0.1\"\n}", 400},
		{s.URL + "/port/22", "10.0.0.1", "{\n  \"status\": 400,\n  \"error\": \"cannot check 10.0.0.1\"\n}", 400},
		{s.URL + "/port/22", "169.254.169.254", "{\n  \"status\": 400,\n  \"error\": \"cannot check 169.254.169.254\"\n}", 400},
		{s.URL + "/port/22", "100.64.1.1", "{\n  \"status\": 400,\n  \"error\": \"cannot check 100.64.1.1\"\n}", 400},
		{s.URL + "/port/22", "::1", "{\n  \"status\": 400,\n  \"error\": \"cannot check ::1\"\n}", 400},
		{s.URL + "/port/22", "fc00::1", "{\n  \"status\": 400,\n  \"error\": \"cannot check fc00::1\"\n}", 400},
	}

	for _, tt := range tests {
		r, err := http.NewRequest(http.MethodGet, tt.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Real-IP", tt.header)
		r.Header.Set("Accept", jsonMediaType)

		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		if res.StatusCode != tt.status {
			t.Errorf("%s from %s: expected %d, got %d", tt.url, tt.header, tt.status, res.StatusCode)
		}
		if string(body) != tt.out {
			t.Errorf("%s from %s: expected %q, got %q", tt.url, tt.header, tt.out, body)
		}
	}
}
