package geo

import (
	"errors"
	"net"
	"net/netip"
	"sync"

	"github.com/oschwald/geoip2-golang/v2"
)

// Reader looks up GeoIP records. The Has methods report which lookups the
// databases currently loaded can answer.
type Reader interface {
	City(net.IP) (City, error)
	ASN(net.IP) (ASN, error)
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

// toAddr converts to the address type expected by geoip2. Unmap keeps
// IPv4-in-IPv6 addresses from missing their IPv4 records.
func toAddr(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (d *Database) City(ip net.IP) (City, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	city := City{}

	if d.city == nil {
		return city, nil
	}

	addr, ok := toAddr(ip)
	if !ok {
		return city, nil
	}

	record, err := d.city.City(addr)

	if err != nil {
		return city, err
	}

	// City Database also includes Country Data
	if c := record.Country.Names.English; c != "" {
		city.CountryName = c
	}

	if c := record.RegisteredCountry.Names.English; c != "" && city.CountryName == "" {
		city.CountryName = c
	}

	if record.Country.ISOCode != "" {
		city.CountryISO = record.Country.ISOCode
	}

	if record.RegisteredCountry.ISOCode != "" && city.CountryISO == "" {
		city.CountryISO = record.RegisteredCountry.ISOCode
	}

	isEU := record.Country.IsInEuropeanUnion || record.RegisteredCountry.IsInEuropeanUnion
	city.CountryIsEU = &isEU

	if c := record.City.Names.English; c != "" {
		city.Name = c
	}

	if len(record.Subdivisions) > 0 {
		if c := record.Subdivisions[0].Names.English; c != "" {
			city.RegionName = c
		}
		if record.Subdivisions[0].ISOCode != "" {
			city.RegionCode = record.Subdivisions[0].ISOCode
		}
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

	if record.Postal.Code != "" {
		city.PostalCode = record.Postal.Code
	}

	if record.Location.TimeZone != "" {
		city.Timezone = record.Location.TimeZone
	}

	return city, nil
}

func (d *Database) ASN(ip net.IP) (ASN, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	asn := ASN{}
	if d.asn == nil {
		return asn, nil
	}

	addr, ok := toAddr(ip)
	if !ok {
		return asn, nil
	}

	record, err := d.asn.ASN(addr)
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
