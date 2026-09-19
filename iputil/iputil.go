package iputil

import (
	"context"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"time"
)

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

// LookupPort reports whether a TCP connection to addr reaches port.
func LookupPort(addr netip.Addr, port uint16) error {
	conn, err := net.DialTimeout("tcp", netip.AddrPortFrom(addr, port).String(), dialTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()

	return nil
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
