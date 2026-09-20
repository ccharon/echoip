package geo

import (
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// databases returns the paths of the GeoLite2 files if they have been
// downloaded, so the test also covers closing readers that are in use.
func databases(t *testing.T) (string, string) {
	t.Helper()

	dir := filepath.Join("..", "..", "data")
	city := filepath.Join(dir, "GeoLite2-City.mmdb")
	asn := filepath.Join(dir, "GeoLite2-ASN.mmdb")

	for _, path := range []string{city, asn} {
		if _, err := os.Stat(path); err != nil {
			return "", ""
		}
	}
	return city, asn
}

func TestReloadDuringLookups(t *testing.T) {
	city, asn := databases(t)

	d, err := Open(city, asn)
	if err != nil {
		t.Fatal(err)
	}

	addr := netip.MustParseAddr("8.8.8.8")
	done := make(chan struct{})

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					if _, err := d.City(addr); err != nil {
						t.Error(err)
						return
					}
					if _, err := d.ASN(addr); err != nil {
						t.Error(err)
						return
					}
					d.HasCity()
					d.HasASN()
				}
			}
		})
	}

	for range 20 {
		if err := d.Reload(); err != nil {
			t.Fatal(err)
		}
	}

	close(done)
	wg.Wait()
}

func TestOpenMissingDatabase(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "absent.mmdb"), "")
	if err == nil {
		t.Fatal("expected error")
	}
	if d.HasCity() || d.HasASN() {
		t.Error("expected no database to be in use")
	}
	if _, err := d.City(netip.MustParseAddr("8.8.8.8")); err != nil {
		t.Errorf("expected lookups on an empty database to succeed: %v", err)
	}
	if _, err := d.ASN(netip.MustParseAddr("8.8.8.8")); err != nil {
		t.Errorf("expected lookups on an empty database to succeed: %v", err)
	}
}

func TestReloadAfterMissingDatabase(t *testing.T) {
	city, asn := databases(t)
	if city == "" {
		t.Skip("GeoLite2 databases not present")
	}

	d, err := Open(filepath.Join(t.TempDir(), "absent.mmdb"), asn)
	if err == nil {
		t.Fatal("expected error")
	}

	// The database that did open is in use, although the other one failed.
	if d.HasCity() {
		t.Error("expected no city database")
	}
	if !d.HasASN() {
		t.Error("expected the ASN database to be in use")
	}

	d.cityPath = city
	if err := d.Reload(); err != nil {
		t.Fatal(err)
	}
	if !d.HasCity() || !d.HasASN() {
		t.Fatal("expected both databases to be in use")
	}

	record, err := d.City(netip.MustParseAddr("8.8.8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if record.CountryISO == "" {
		t.Error("expected a country after the database loaded")
	}
}

func TestReloadKeepsDatabasesOnError(t *testing.T) {
	city, asn := databases(t)
	if city == "" {
		t.Skip("GeoLite2 databases not present")
	}

	d, err := Open(city, asn)
	if err != nil {
		t.Fatal(err)
	}

	d.cityPath = filepath.Join(t.TempDir(), "absent.mmdb")
	if err := d.Reload(); err == nil {
		t.Fatal("expected error")
	}

	// The previous databases are still usable.
	record, err := d.City(netip.MustParseAddr("8.8.8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if record.CountryISO == "" {
		t.Error("expected a country after a failed reload")
	}
}

// An address the database does not know must leave every field empty, so the
// JSON response carries none of them.
func TestCityWithoutData(t *testing.T) {
	cityFile, asnFile := databases(t)
	if cityFile == "" {
		t.Skip("GeoLite2 databases are not downloaded")
	}

	d, err := Open(cityFile, asnFile)
	if err != nil {
		t.Fatal(err)
	}
	city, err := d.City(netip.MustParseAddr("10.0.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if city.CountryIsEU != nil {
		t.Errorf("CountryIsEU = %t, want nil", *city.CountryIsEU)
	}
	if city != (City{}) {
		t.Errorf("expected the zero value, got %+v", city)
	}
}

// A country that is not in the EU still reports the field, because false is an
// answer there.
func TestCityOutsideEU(t *testing.T) {
	cityFile, asnFile := databases(t)
	if cityFile == "" {
		t.Skip("GeoLite2 databases are not downloaded")
	}

	d, err := Open(cityFile, asnFile)
	if err != nil {
		t.Fatal(err)
	}
	city, err := d.City(netip.MustParseAddr("8.8.8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if city.CountryIsEU == nil {
		t.Fatal("expected CountryIsEU to be set")
	}
	if *city.CountryIsEU {
		t.Error("expected the United States to be outside the EU")
	}
}
