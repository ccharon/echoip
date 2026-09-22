// Command echoip answers HTTP requests with the IP address of the caller and
// its GeoIP data.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ccharon/echoip/iputil"
	"github.com/ccharon/echoip/iputil/geo"
	"github.com/ccharon/echoip/maxmind"
	"github.com/ccharon/echoip/server"
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
// edition is unchanged. The interval is whole hours, because MaxMind counts
// downloads per day and nothing below that resolution is useful here.
const defaultUpdateHours = 24

type options struct {
	cityFile       string
	asnFile        string
	listen         string
	headers        []string
	trustedProxies []netip.Prefix
	cacheSize      int
	updateHours    int
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
	fs.IntVar(&opts.updateHours, "u", defaultUpdateHours,
		"Hours between checks for new GeoIP databases. Requires "+accountIDEnv+" and "+
			licenseKeyEnv+". 0 disables checking and asks for neither")
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
	if opts.updateHours < 0 {
		fs.Usage()
		return nil, fmt.Errorf("-u must not be negative: %d", opts.updateHours)
	}

	return &opts, nil
}

// updateInterval is the flag value as a duration.
func (o *options) updateInterval() time.Duration {
	return time.Duration(o.updateHours) * time.Hour
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

func main() {
	// The request log is an audit trail, so every line needs its own time. A
	// supervisor that stamps as well leaves the two side by side.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

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

	if err := run(opts); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}

// run serves until SIGINT or SIGTERM.
func run(opts *options) error {
	slog.Info("starting echoip", "version", version)

	updater := &maxmind.Updater{
		AccountID:  os.Getenv(accountIDEnv),
		LicenseKey: os.Getenv(licenseKeyEnv),
		Interval:   opts.updateInterval(),
		Databases:  editions(opts.cityFile, opts.asnFile),
	}
	if env := missingCredential(updater); env != "" {
		return fmt.Errorf("%s must be set to download the GeoIP databases", env)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if updater.Enabled() && updater.Stale() {
		slog.Info("checking GeoIP databases")
		if _, err := updater.Update(ctx); err != nil {
			slog.Error("GeoIP update failed", "error", err)
		}
	}

	// A missing or broken database only disables the lookups that need it.
	geoReader, err := geo.Open(opts.cityFile, opts.asnFile)
	if err != nil {
		slog.Warn("GeoIP lookups are limited", "error", err)
	}

	cache := server.NewCache(opts.cacheSize)
	srv := server.New(serverConfig(opts), geoReader, cache)

	if updater.Enabled() {
		slog.Info("checking GeoIP databases periodically", "interval", updater.Interval)
		go updater.Run(ctx, func() error {
			if err := geoReader.Reload(); err != nil {
				return err
			}
			// Cached responses were built from the previous databases.
			cache.Clear()
			return nil
		})
	}

	slog.Info("listening", "addr", opts.listen)
	if err := srv.ListenAndServe(ctx, opts.listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("shutdown complete")
	return nil
}

func prefixList(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

// missingCredential names the environment variable the updater needs and does
// not have. An updater that never runs needs none, which leaves the database
// files to whatever put them there.
func missingCredential(u *maxmind.Updater) string {
	if len(u.Databases) == 0 || u.Interval <= 0 {
		return ""
	}
	if u.AccountID == "" {
		return accountIDEnv
	}
	if u.LicenseKey == "" {
		return licenseKeyEnv
	}
	return ""
}

// serverConfig turns the flags into the server configuration and reports what
// it enabled.
func serverConfig(opts *options) server.Config {
	cfg := server.Config{
		IPHeaders:      opts.headers,
		TrustedProxies: opts.trustedProxies,
		Profile:        opts.profile,
		City:           opts.cityFile != "",
		ASN:            opts.asnFile != "",
	}

	if opts.reverseLookup {
		slog.Info("reverse lookup enabled")
		cfg.LookupAddr = iputil.LookupAddr
	}

	if len(opts.headers) > 0 {
		headers := strings.Join(opts.headers, ", ")
		if len(opts.trustedProxies) == 0 {
			slog.Warn("any caller can set the trusted headers, set -T to limit them to the proxy", "headers", headers)
		} else {
			slog.Info("trusting headers from proxies", "headers", headers, "proxies", prefixList(opts.trustedProxies))
		}
	}

	if opts.cacheSize > 0 {
		slog.Info("cache enabled", "capacity", opts.cacheSize)
	}

	if opts.profile {
		slog.Warn("profiling handlers on /debug are unauthenticated and expose memory contents, "+
			"keep the listen address off the internet", "addr", opts.listen)
	}

	return cfg
}
