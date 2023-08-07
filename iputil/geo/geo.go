package geo

import (
	"math"
	"net"

	"github.com/oschwald/geoip2-golang"
)

type Reader interface {
	City(net.IP) (City, error)
	ASN(net.IP) (ASN, error)
	IsEmpty() bool
}
type City struct {
	Name        string
	Latitude    float64
	Longitude   float64
	PostalCode  string
	Timezone    string
	MetroCode   uint
	RegionName  string
	RegionCode  string
	CountryName string
	CountryISO  string
	CountryIsEU *bool
}

type ASN struct {
	AutonomousSystemNumber       uint
	AutonomousSystemOrganization string
}

type geoip struct {
	city *geoip2.Reader
	asn  *geoip2.Reader
}

func Open(cityDB string, asnDB string) (Reader, error) {
	var city, asn *geoip2.Reader

	if cityDB != "" {
		r, err := geoip2.Open(cityDB)
		if err != nil {
			return nil, err
		}
		city = r
	}

	if asnDB != "" {
		r, err := geoip2.Open(asnDB)
		if err != nil {
			return nil, err
		}
		asn = r
	}

	return &geoip{city: city, asn: asn}, nil
}

func (g *geoip) City(ip net.IP) (City, error) {
	city := City{}

	if g.city == nil {
		return city, nil
	}

	record, err := g.city.City(ip)

	if err != nil {
		return city, err
	}

	// City Database also includes Country Data
	if c, exists := record.Country.Names["en"]; exists {
		city.CountryName = c
	}

	if c, exists := record.RegisteredCountry.Names["en"]; exists && city.CountryName == "" {
		city.CountryName = c
	}

	if record.Country.IsoCode != "" {
		city.CountryISO = record.Country.IsoCode
	}

	if record.RegisteredCountry.IsoCode != "" && city.CountryISO == "" {
		city.CountryISO = record.RegisteredCountry.IsoCode
	}

	isEU := record.Country.IsInEuropeanUnion || record.RegisteredCountry.IsInEuropeanUnion
	city.CountryIsEU = &isEU

	if c, exists := record.City.Names["en"]; exists {
		city.Name = c
	}

	if len(record.Subdivisions) > 0 {
		if c, exists := record.Subdivisions[0].Names["en"]; exists {
			city.RegionName = c
		}
		if record.Subdivisions[0].IsoCode != "" {
			city.RegionCode = record.Subdivisions[0].IsoCode
		}
	}

	if !math.IsNaN(record.Location.Latitude) {
		city.Latitude = record.Location.Latitude
	}

	if !math.IsNaN(record.Location.Longitude) {
		city.Longitude = record.Location.Longitude
	}

	// Metro code is US Only https://maxmind.github.io/GeoIP2-dotnet/doc/v2.7.1/html/P_MaxMind_GeoIP2_Model_Location_MetroCode.htm
	if record.Location.MetroCode > 0 && record.Country.IsoCode == "US" {
		city.MetroCode = record.Location.MetroCode
	}

	if record.Postal.Code != "" {
		city.PostalCode = record.Postal.Code
	}

	if record.Location.TimeZone != "" {
		city.Timezone = record.Location.TimeZone
	}

	return city, nil
}

func (g *geoip) ASN(ip net.IP) (ASN, error) {
	asn := ASN{}
	if g.asn == nil {
		return asn, nil
	}

	record, err := g.asn.ASN(ip)
	if err != nil {
		return asn, err
	}

	if record.AutonomousSystemNumber > 0 {
		asn.AutonomousSystemNumber = record.AutonomousSystemNumber
	}
	if record.AutonomousSystemOrganization != "" {
		asn.AutonomousSystemOrganization = record.AutonomousSystemOrganization
	}

	return asn, nil
}

func (g *geoip) IsEmpty() bool {
	return g.city == nil
}
