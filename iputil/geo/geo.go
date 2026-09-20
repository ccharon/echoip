package geo

import (
	"errors"
	"net/netip"
	"sync"

	"github.com/oschwald/geoip2-golang/v2"
)

// Reader looks up GeoIP records. The Has methods report which lookups the
// databases currently loaded can answer.
type Reader interface {
	City(netip.Addr) (City, error)
	ASN(netip.Addr) (ASN, error)
	HasCity() bool
	HasASN() bool
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

// Database reads GeoIP records from files that may be replaced while the
// server runs. Every lookup holds mu for its whole duration, so Reload cannot
// close a database that is still in use.
type Database struct {
	cityPath string
	asnPath  string

	mu   sync.RWMutex
	city *geoip2.Reader
	asn  *geoip2.Reader
}

// Open returns a Database reading from the given files. An empty path leaves
// that database out. A file that cannot be opened is reported as an error, and
// the returned Database serves the lookups of the databases that did open.
func Open(cityDB string, asnDB string) (*Database, error) {
	d := &Database{cityPath: cityDB, asnPath: asnDB}
	return d, d.Reload()
}

// Reload opens the database files again and replaces the handles that could be
// opened. A database that fails to open keeps the handle it had, so a half
// written or corrupt file does not take working lookups down.
func (d *Database) Reload() error {
	city, cityErr := openReader(d.cityPath)
	asn, asnErr := openReader(d.asnPath)

	d.mu.Lock()
	oldCity, oldASN := d.city, d.asn
	if cityErr == nil {
		d.city = city
	}
	if asnErr == nil {
		d.asn = asn
	}
	d.mu.Unlock()

	if cityErr == nil {
		closeReader(oldCity)
	} else {
		closeReader(city)
	}
	if asnErr == nil {
		closeReader(oldASN)
	} else {
		closeReader(asn)
	}

	return errors.Join(cityErr, asnErr)
}

// openReader returns no reader for an empty path, which leaves that database
// out.
func openReader(path string) (*geoip2.Reader, error) {
	if path == "" {
		return nil, nil
	}
	return geoip2.Open(path)
}

func closeReader(r *geoip2.Reader) {
	if r != nil {
		_ = r.Close()
	}
}

func (d *Database) City(addr netip.Addr) (City, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	city := City{}

	if d.city == nil || !addr.IsValid() {
		return city, nil
	}

	// Unmap so an IPv4-in-IPv6 address finds its IPv4 records.
	record, err := d.city.City(addr.Unmap())
	if err != nil {
		return city, err
	}

	// The city database carries country records too. The registered country
	// stands in when the country itself is unknown.
	city.CountryName = record.Country.Names.English
	if city.CountryName == "" {
		city.CountryName = record.RegisteredCountry.Names.English
	}

	city.CountryISO = record.Country.ISOCode
	if city.CountryISO == "" {
		city.CountryISO = record.RegisteredCountry.ISOCode
	}

	// Only the country decides EU membership, because the registered country
	// says where the block is registered rather than where it is used. It is
	// left out entirely when no country was found.
	if city.CountryName != "" || city.CountryISO != "" {
		isEU := record.Country.IsInEuropeanUnion
		city.CountryIsEU = &isEU
	}

	city.Name = record.City.Names.English

	if len(record.Subdivisions) > 0 {
		city.RegionName = record.Subdivisions[0].Names.English
		city.RegionCode = record.Subdivisions[0].ISOCode
	}

	if record.Location.Latitude != nil {
		city.Latitude = *record.Location.Latitude
	}

	if record.Location.Longitude != nil {
		city.Longitude = *record.Location.Longitude
	}

	// Metro code is US Only https://maxmind.github.io/GeoIP2-dotnet/doc/v2.7.1/html/P_MaxMind_GeoIP2_Model_Location_MetroCode.htm
	if record.Location.MetroCode > 0 && record.Country.ISOCode == "US" {
		city.MetroCode = record.Location.MetroCode
	}

	city.PostalCode = record.Postal.Code
	city.Timezone = record.Location.TimeZone

	return city, nil
}

func (d *Database) ASN(addr netip.Addr) (ASN, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	asn := ASN{}
	if d.asn == nil || !addr.IsValid() {
		return asn, nil
	}

	record, err := d.asn.ASN(addr.Unmap())
	if err != nil {
		return asn, err
	}

	asn.AutonomousSystemNumber = record.AutonomousSystemNumber
	asn.AutonomousSystemOrganization = record.AutonomousSystemOrganization

	return asn, nil
}

func (d *Database) HasCity() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.city != nil
}

func (d *Database) HasASN() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.asn != nil
}
