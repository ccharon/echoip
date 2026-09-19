package iputil

import (
	"context"
	"math/big"
	"net"
	"strconv"
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

// LookupAddr returns the hostname of ip, without the trailing dot of the
// resolver answer.
func LookupAddr(ip net.IP) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lookupAddrTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, ip.String())
	if err != nil || len(names) == 0 {
		return "", err
	}
	return strings.TrimSuffix(names[0], "."), nil
}

// LookupPort reports whether a TCP connection to ip reaches port.
func LookupPort(ip net.IP, port uint64) error {
	address := net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10))

	conn, err := net.DialTimeout("tcp", address, dialTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()

	return nil
}

// ToDecimal returns the address as a number, IPv4 from its 4 bytes so that the
// value matches the usual decimal notation.
func ToDecimal(ip net.IP) *big.Int {
	i := big.NewInt(0)
	if to4 := ip.To4(); to4 != nil {
		i.SetBytes(to4)
	} else {
		i.SetBytes(ip)
	}
	return i
}
