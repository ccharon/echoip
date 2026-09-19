package main

import (
	"context"
	"errors"
	"flag"
	"log"
	stdhttp "net/http"
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

// licenseKeyEnv holds the MaxMind license key. It is read from the
// environment rather than a flag, because flags are visible in the process
// list.
const licenseKeyEnv = "GEOIP_LICENSE_KEY"

// MaxMind rebuilds GeoLite2 twice a week. A daily check keeps the data at most
// a day behind, and costs nothing while the edition is unchanged, because the
// request is conditional.
const defaultUpdateInterval = 24 * time.Hour

type options struct {
	cityFile       string
	asnFile        string
	listen         string
	templateDir    string
	headers        []string
	cacheSize      int
	updateInterval time.Duration
	reverseLookup  bool
	portLookup     bool
	profile        bool
}

func parseFlags(args []string) (*options, error) {
	var opts options

	fs := flag.NewFlagSet("echoip", flag.ContinueOnError)
	fs.StringVar(&opts.cityFile, "c", "", "Path to GeoIP city database")
	fs.StringVar(&opts.asnFile, "a", "", "Path to GeoIP ASN database")
	fs.StringVar(&opts.listen, "l", ":8080", "Listening address")
	fs.BoolVar(&opts.reverseLookup, "r", false, "Perform reverse hostname lookups")
	fs.BoolVar(&opts.portLookup, "p", false, "Enable port lookup")
	fs.StringVar(&opts.templateDir, "t", "html", "Path to template dir")
	fs.IntVar(&opts.cacheSize, "C", 0, "Size of response cache. Set to 0 to disable")
	fs.BoolVar(&opts.profile, "P", false, "Enables profiling handlers")
	fs.DurationVar(&opts.updateInterval, "u", defaultUpdateInterval,
		"Interval for checking MaxMind for new GeoIP databases. Requires "+licenseKeyEnv+". Set to 0 to disable")
	fs.Func("H", "Header to trust for remote IP, if present (e.g. X-Real-IP)", func(v string) error {
		opts.headers = append(opts.headers, v)
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
	log.SetFlags(log.Lshortfile)
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}

	updater := &maxmind.Updater{
		LicenseKey: os.Getenv(licenseKeyEnv),
		Interval:   opts.updateInterval,
		Databases:  editions(opts.cityFile, opts.asnFile),
	}
	if len(updater.Databases) > 0 && updater.LicenseKey == "" {
		log.Fatalf("%s must be set to download the GeoIP databases", licenseKeyEnv)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if updater.Enabled() && updater.Stale() {
		log.Print("Checking GeoIP databases")
		if _, err := updater.Update(ctx); err != nil {
			log.Print(err)
		}
	}

	// A database that is missing or broken only disables the lookups that need
	// it. The server keeps serving and picks the database up once a refresh
	// succeeds.
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

// serverConfig turns the flags into the server configuration and reports what
// it enabled.
func serverConfig(opts *options) http.Config {
	cfg := http.Config{
		IPHeaders: opts.headers,
		Profile:   opts.profile,
	}

	if _, err := os.Stat(opts.templateDir); err == nil {
		cfg.TemplateDir = opts.templateDir
	} else {
		log.Printf("Not configuring default handler: Template not found: %s", opts.templateDir)
	}

	if opts.reverseLookup {
		log.Print("Enabling reverse lookup")
		cfg.LookupAddr = iputil.LookupAddr
	}

	if opts.portLookup {
		log.Print("Enabling port lookup")
		cfg.LookupPort = iputil.LookupPort
	}

	if len(opts.headers) > 0 {
		log.Printf("Trusting remote IP from header(s): %s", strings.Join(opts.headers, ", "))
	}

	if opts.cacheSize > 0 {
		log.Printf("Cache capacity set to %d", opts.cacheSize)
	}

	if opts.profile {
		log.Print("Enabling profiling handlers")
	}

	return cfg
}
