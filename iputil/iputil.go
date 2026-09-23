// Package iputil classifies IP addresses, formats them and resolves their
// hostnames.
package iputil

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Bounds the reverse lookup, which runs while a request waits for its
// response.
const lookupAddrTimeout = 2 * time.Second

// LookupAddr returns the hostname of addr, without the trailing dot of the
// resolver answer.
func LookupAddr(ctx context.Context, addr netip.Addr) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupAddrTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, addr.String())
	if err != nil || len(names) == 0 {
		return "", err
	}
	return strings.TrimSuffix(names[0], "."), nil
}

// notGloballyReachable holds the special-purpose prefixes that netip.Addr does
// not classify on its own.
var notGloballyReachable = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // this network
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space, carrier grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, holds the broadcast address
	netip.MustParsePrefix("100::/64"),        // discard only
	netip.MustParsePrefix("2001:2::/48"),     // benchmarking
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("3fff::/20"),       // documentation
	netip.MustParsePrefix("5f00::/16"),       // segment routing SIDs
}

// Public reports whether addr is a globally reachable unicast address, which
// is what the service answers for.
func Public(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}

	// netip.Prefix.Contains treats a mapped address as IPv6, so the IPv4
	// prefixes below would never match it.
	addr = addr.Unmap()

	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsMulticast() {
		return false
	}
	for _, prefix := range notGloballyReachable {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
