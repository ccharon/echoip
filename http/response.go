package http

import (
	"fmt"
	"math/big"
	"net"
	"net/http"
	"path"
	"strconv"

	"github.com/ccharon/echoip/iputil"
	"github.com/ccharon/echoip/useragent"
)

type Response struct {
	IP         net.IP               `json:"ip"`
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

type PortResponse struct {
	IP        net.IP `json:"ip"`
	Port      uint64 `json:"port"`
	Reachable bool   `json:"reachable"`
}

// Coordinates formats latitude and longitude the way the CLI response prints
// them.
func (r Response) Coordinates() string {
	return formatCoordinate(r.Latitude) + "," + formatCoordinate(r.Longitude)
}

func formatCoordinate(c float64) string {
	return strconv.FormatFloat(c, 'f', 6, 64)
}

// newResponse builds the response for this request, from the cache when the
// address is known. The user agent is never cached, because it belongs to the
// request rather than to the address.
func (s *Server) newResponse(r *http.Request) (Response, error) {
	ip, err := ipFromRequest(s.cfg.IPHeaders, r, true)
	if err != nil {
		return Response{}, err
	}

	if response, ok := s.cache.Get(ip); ok {
		response.UserAgent = userAgentFromRequest(r)
		return response, nil
	}

	city, _ := s.geo.City(ip)
	asn, _ := s.geo.ASN(ip)

	var hostname string
	if s.cfg.LookupAddr != nil {
		hostname, _ = s.cfg.LookupAddr(ip)
	}

	var asnumber string
	if asn.AutonomousSystemNumber > 0 {
		asnumber = fmt.Sprintf("AS%d", asn.AutonomousSystemNumber)
	}

	response := Response{
		IP:         ip,
		IPDecimal:  iputil.ToDecimal(ip),
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

	s.cache.Set(ip, response)
	response.UserAgent = userAgentFromRequest(r)

	return response, nil
}

func (s *Server) newPortResponse(r *http.Request) (PortResponse, error) {
	lastElement := path.Base(r.URL.Path)

	port, err := strconv.ParseUint(lastElement, 10, 16)
	if err != nil || port == 0 {
		return PortResponse{Port: port}, fmt.Errorf("invalid port: %s", lastElement)
	}

	ip, err := ipFromRequest(s.cfg.IPHeaders, r, false)
	if err != nil {
		return PortResponse{Port: port}, err
	}

	return PortResponse{
		IP:        ip,
		Port:      port,
		Reachable: s.cfg.LookupPort(ip, port) == nil,
	}, nil
}
