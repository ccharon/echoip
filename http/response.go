package http

import (
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/ccharon/echoip/iputil"
	"github.com/ccharon/echoip/useragent"
)

type Response struct {
	IP         netip.Addr           `json:"ip"`
	IPDecimal  *big.Int             `json:"ip_decimal"`
	Country    string               `json:"country,omitempty"`
	CountryISO string               `json:"country_iso,omitempty"`
	CountryEU  *bool                `json:"country_eu,omitempty"`
	RegionName string               `json:"region_name,omitempty"`
	RegionCode string               `json:"region_code,omitempty"`
	MetroCode  uint                 `json:"metro_code,omitempty"`
	PostalCode string               `json:"zip_code,omitempty"`
	City       string               `json:"city,omitempty"`
	Latitude   float64              `json:"latitude,omitempty"`
	Longitude  float64              `json:"longitude,omitempty"`
	Timezone   string               `json:"time_zone,omitempty"`
	ASN        string               `json:"asn,omitempty"`
	ASNOrg     string               `json:"asn_org,omitempty"`
	Hostname   string               `json:"hostname,omitempty"`
	UserAgent  *useragent.UserAgent `json:"user_agent,omitempty"`
}

// Coordinates formats latitude and longitude the way the CLI response prints
// them.
func (r Response) Coordinates() string {
	return formatCoordinate(r.Latitude) + "," + formatCoordinate(r.Longitude)
}

func formatCoordinate(c float64) string {
	return strconv.FormatFloat(c, 'f', 6, 64)
}

// newResponse answers from the cache when the address is known. The user agent
// belongs to the request, not to the address, so it is never cached.
func (s *Server) newResponse(r *http.Request) (Response, error) {
	addr, err := s.ipFromRequest(r, true)
	if err != nil {
		return Response{}, err
	}

	if response, ok := s.cache.Get(addr); ok {
		response.UserAgent = userAgentFromRequest(r)
		return response, nil
	}

	city, _ := s.geo.City(addr)
	asn, _ := s.geo.ASN(addr)

	var hostname string
	if s.cfg.LookupAddr != nil {
		hostname, _ = s.cfg.LookupAddr(addr)
	}

	var asnumber string
	if asn.AutonomousSystemNumber > 0 {
		asnumber = fmt.Sprintf("AS%d", asn.AutonomousSystemNumber)
	}

	response := Response{
		IP:         addr,
		IPDecimal:  iputil.ToDecimal(addr),
		Country:    city.CountryName,
		CountryISO: city.CountryISO,
		CountryEU:  city.CountryIsEU,
		RegionName: city.RegionName,
		RegionCode: city.RegionCode,
		MetroCode:  city.MetroCode,
		PostalCode: city.PostalCode,
		City:       city.Name,
		Latitude:   city.Latitude,
		Longitude:  city.Longitude,
		Timezone:   city.Timezone,
		ASN:        asnumber,
		ASNOrg:     asn.AutonomousSystemOrganization,
		Hostname:   hostname,
	}

	s.cache.Set(addr, response)
	response.UserAgent = userAgentFromRequest(r)

	return response, nil
}
