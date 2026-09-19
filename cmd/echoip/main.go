package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	stdhttp "net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ccharon/echoip/http"
	"github.com/ccharon/echoip/iputil"
	"github.com/ccharon/echoip/iputil/geo"
	"github.com/ccharon/echoip/maxmind"
)

// version is set at build time from the git tag.
var version = "dev"

// The MaxMind credentials. They are read from the environment rather than
// from flags, because flags are visible in the process list.
const (
	accountIDEnv  = "MAXMIND_ACCOUNT_ID"
	licenseKeyEnv = "GEOIP_LICENSE_KEY"
)

// MaxMind rebuilds GeoLite2 twice a week, and a check costs nothing while the
// edition is unchanged.
const defaultUpdateInterval = 24 * time.Hour

type options struct {
	cityFile       string
	asnFile        string
	listen         string
	headers        []string
	trustedProxies []netip.Prefix
	cacheSize      int
	updateInterval time.Duration
	reverseLookup  bool
	profile        bool
	showVersion    bool
}

// parsePrefix accepts a network in CIDR notation or a single address. Host
// bits in a network are an error, so a typo cannot widen it silently.
func parsePrefix(v string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(v); err == nil {
		// Supplied addresses are unmapped, so a mapped prefix matches nothing.
		if prefix.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf("write %s as an IPv4 network", v)
		}
		if masked := prefix.Masked(); prefix != masked {
			return netip.Prefix{}, fmt.Errorf("%s has host bits set, write %s or a single address", v, masked)
		}
		return prefix, nil
	}
	addr, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("not an address or network: %s", v)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func parseFlags(args []string, output io.Writer) (*options, error) {
	var opts options

	fs := flag.NewFlagSet("echoip", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.cityFile, "c", "", "Path to the GeoIP city database")
	fs.StringVar(&opts.asnFile, "a", "", "Path to the GeoIP ASN database")
	fs.StringVar(&opts.listen, "l", ":8080", "Listening address")
	fs.BoolVar(&opts.reverseLookup, "r", false, "Perform reverse hostname lookups")
	fs.IntVar(&opts.cacheSize, "C", 0, "Size of the response cache. 0 disables caching")
	fs.BoolVar(&opts.profile, "P", false, "Register the pprof and cache handlers below /debug")
	fs.DurationVar(&opts.updateInterval, "u", defaultUpdateInterval,
		"Interval for checking MaxMind for new GeoIP databases. Requires "+accountIDEnv+" and "+
			licenseKeyEnv+". 0 disables checking")
	fs.BoolVar(&opts.showVersion, "V", false, "Print the version and exit")
	fs.Func("H", "Header to trust for the remote IP, e.g. X-Real-IP. May be repeated", func(v string) error {
		opts.headers = append(opts.headers, v)
		return nil
	})
	fs.Func("T", "Network whose requests may set the headers from -H, e.g. 10.0.0.0/8 or a single "+
		"address. May be repeated. Without it every peer may set them", func(v string) error {
		prefix, err := parsePrefix(v)
		if err != nil {
			return err
		}
		opts.trustedProxies = append(opts.trustedProxies, prefix)
		return nil
	})

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return nil, errors.New("unexpected arguments")
	}

	return &opts, nil
}

// editions maps the GeoLite2 editions the updater downloads to the files the
// server reads.
func editions(cityFile, asnFile string) map[string]string {
	e := make(map[string]string)
	if cityFile != "" {
		e[maxmind.EditionCity] = cityFile
	}
	if asnFile != "" {
		e[maxmind.EditionASN] = asnFile
	}
	return e
}

func init() {
	log.SetPrefix("echoip: ")
	// The request log is an audit trail, so every line needs its own time. A
	// supervisor that stamps as well leaves the two side by side.
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func main() {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}

	if opts.showVersion {
		fmt.Println(version)
		return
	}

	log.Printf("echoip %s", version)

	updater := &maxmind.Updater{
		AccountID:  os.Getenv(accountIDEnv),
		LicenseKey: os.Getenv(licenseKeyEnv),
		Interval:   opts.updateInterval,
		Databases:  editions(opts.cityFile, opts.asnFile),
	}
	if len(updater.Databases) > 0 {
		for env, value := range map[string]string{accountIDEnv: updater.AccountID, licenseKeyEnv: updater.LicenseKey} {
			if value == "" {
				log.Fatalf("%s must be set to download the GeoIP databases", env)
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if updater.Enabled() && updater.Stale() {
		log.Print("Checking GeoIP databases")
		if _, err := updater.Update(ctx); err != nil {
			log.Print(err)
		}
	}

	// A missing or broken database only disables the lookups that need it.
	geoReader, err := geo.Open(opts.cityFile, opts.asnFile)
	if err != nil {
		log.Printf("GeoIP lookups are limited: %v", err)
	}

	cache := http.NewCache(opts.cacheSize)
	server := http.New(serverConfig(opts), geoReader, cache)

	if updater.Enabled() {
		log.Printf("Checking GeoIP databases every %s", updater.Interval)
		go updater.Run(ctx, func() error {
			if err := geoReader.Reload(); err != nil {
				return err
			}
			// Cached responses were built from the previous databases.
			cache.Clear()
			return nil
		})
	}

	log.Printf("Listening on http://%s", opts.listen)
	if err := server.ListenAndServe(ctx, opts.listen); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("Shutdown complete")
}

func prefixList(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

// serverConfig turns the flags into the server configuration and reports what
// it enabled.
func serverConfig(opts *options) http.Config {
	cfg := http.Config{
		IPHeaders:      opts.headers,
		TrustedProxies: opts.trustedProxies,
		Profile:        opts.profile,
	}

	if opts.reverseLookup {
		log.Print("Enabling reverse lookup")
		cfg.LookupAddr = iputil.LookupAddr
	}

	if len(opts.headers) > 0 {
		log.Printf("Trusting remote IP from header(s): %s", strings.Join(opts.headers, ", "))
		if len(opts.trustedProxies) == 0 {
			log.Print("Any caller can set those headers. Set -T to limit them to the proxy")
		} else {
			log.Printf("Headers are only read from: %s", prefixList(opts.trustedProxies))
		}
	}

	if opts.cacheSize > 0 {
		log.Printf("Cache capacity set to %d", opts.cacheSize)
	}

	if opts.profile {
		log.Printf("Enabling profiling handlers on /debug. They are not "+
			"authenticated and expose memory contents, so %s must not be "+
			"reachable from the internet while they are on", opts.listen)
	}

	return cfg
}
