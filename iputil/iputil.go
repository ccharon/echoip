package iputil

import (
	"context"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"time"
)

// ErrNotRoutable is returned for a target that the service must not connect
// to.
var ErrNotRoutable = errors.New("address is not a routable target")

// Carrier grade NAT is not private by RFC 1918, but it addresses provider
// infrastructure and is a known way into networks the service should not
// reach.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

const (
	// Bounds the reverse lookup, which runs while a request waits for its
	// response.
	lookupAddrTimeout = 2 * time.Second

	// Bounds the connect attempt of a port check.
	dialTimeout = 2 * time.Second
)

// LookupAddr returns the hostname of addr, without the trailing dot of the
// resolver answer.
func LookupAddr(addr netip.Addr) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lookupAddrTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, addr.String())
	if err != nil || len(names) == 0 {
		return "", err
	}
	return strings.TrimSuffix(names[0], "."), nil
}

// LookupPort reports whether a TCP connection to addr reaches port. Only
// addresses that are routable on the internet are accepted, so a caller who
// controls the reported address cannot aim the check at the network the
// service runs in.
func LookupPort(addr netip.Addr, port uint16) error {
	if !Routable(addr) {
		return ErrNotRoutable
	}

	conn, err := net.DialTimeout("tcp", netip.AddrPortFrom(addr, port).String(), dialTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()

	return nil
}

// Routable reports whether addr is reachable over the internet, which rules
// out loopback, private, link local, multicast and carrier grade NAT
// addresses.
func Routable(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() &&
		addr.IsGlobalUnicast() &&
		!addr.IsPrivate() &&
		!cgnat.Contains(addr)
}

// ToDecimal returns the address as a number, an IPv4 address from its 4 bytes
// so that the value matches the usual decimal notation.
func ToDecimal(addr netip.Addr) *big.Int {
	i := new(big.Int)
	if addr.Is4() {
		b := addr.As4()
		return i.SetBytes(b[:])
	}
	b := addr.As16()
	return i.SetBytes(b[:])
}
