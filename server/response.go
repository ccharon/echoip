package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/ccharon/echoip/useragent"
)

// Response is what the service reports about an address, as JSON and in the
// page.
type Response struct {
	IP         netip.Addr           `json:"ip"`
	Country    string               `json:"country,omitempty"`
	CountryISO string               `json:"country_iso,omitempty"`
	CountryEU  *bool                `json:"country_eu,omitempty"`
	RegionName string               `json:"region_name,omitempty"`
	RegionCode string               `json:"region_code,omitempty"`
	PostalCode string               `json:"zip_code,omitempty"`
	City       string               `json:"city,omitempty"`
	Latitude   *float64             `json:"latitude,omitempty"`
	Longitude  *float64             `json:"longitude,omitempty"`
	Timezone   string               `json:"time_zone,omitempty"`
	ASN        string               `json:"asn,omitempty"`
	ASNOrg     string               `json:"asn_org,omitempty"`
	Hostname   string               `json:"hostname,omitempty"`
	UserAgent  *useragent.UserAgent `json:"user_agent,omitempty"`
}

// Coordinates formats latitude and longitude the way the CLI response prints
// them, or nothing without a location.
func (r Response) Coordinates() string {
	if r.Latitude == nil || r.Longitude == nil {
		return ""
	}
	return formatCoordinate(*r.Latitude) + "," + formatCoordinate(*r.Longitude)
}

func formatCoordinate(c float64) string {
	return strconv.FormatFloat(c, 'f', 6, 64)
}

// newResponse answers from the cache when the address is known.
func (s *Server) newResponse(r *http.Request) (Response, *appError) {
	addr, e := s.ipFromRequest(r)
	if e != nil {
		return Response{}, e
	}

	response, cached := s.cache.get(addr)
	if !cached {
		response = s.lookup(r.Context(), addr)
		// A client that left may have cut the reverse lookup short.
		if r.Context().Err() == nil {
			s.cache.set(addr, response)
		}
	}

	// The user agent belongs to the request, not to the address, so it is
	// never cached.
	response.UserAgent = userAgentFromRequest(r)

	return response, nil
}

// lookup gathers what the databases and the resolver know about addr. A lookup
// that fails leaves its fields empty, which is what a missing database gives
// as well.
func (s *Server) lookup(ctx context.Context, addr netip.Addr) Response {
	// An address the database does not hold is no error, so a failure here
	// means the file itself is unreadable and the operator wants to know.
	city, err := s.geo.City(addr)
	if err != nil {
		slog.Error("city lookup failed", "ip", addr, "error", err)
	}
	asn, err := s.geo.ASN(addr)
	if err != nil {
		slog.Error("ASN lookup failed", "ip", addr, "error", err)
	}

	var hostname string
	if s.cfg.LookupAddr != nil {
		hostname, _ = s.cfg.LookupAddr(ctx, addr)
	}

	var asnumber string
	if asn.AutonomousSystemNumber > 0 {
		asnumber = fmt.Sprintf("AS%d", asn.AutonomousSystemNumber)
	}

	return Response{
		IP:         addr,
		Country:    city.CountryName,
		CountryISO: city.CountryISO,
		CountryEU:  city.CountryIsEU,
		RegionName: city.RegionName,
		RegionCode: city.RegionCode,
		PostalCode: city.PostalCode,
		City:       city.Name,
		Latitude:   city.Latitude,
		Longitude:  city.Longitude,
		Timezone:   city.Timezone,
		ASN:        asnumber,
		ASNOrg:     asn.AutonomousSystemOrganization,
		Hostname:   hostname,
	}
}
