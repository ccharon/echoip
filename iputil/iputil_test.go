package iputil

import (
	"errors"
	"math/big"
	"net/netip"
	"testing"
)

func TestToDecimal(t *testing.T) {
	msb := new(big.Int)
	msb, _ = msb.SetString("80000000000000000000000000000000", 16)

	tests := []struct {
		in  string
		out *big.Int
	}{
		{"127.0.0.1", big.NewInt(2130706433)},
		{"::ffff:127.0.0.1", big.NewInt(2130706433)},
		{"::1", big.NewInt(1)},
		{"8000::", msb},
	}
	for _, tt := range tests {
		addr, err := netip.ParseAddr(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		i := ToDecimal(addr.Unmap())
		if tt.out.Cmp(i) != 0 {
			t.Errorf("Expected %d, got %d for IP %s", tt.out, i, tt.in)
		}
	}
}

func TestRoutable(t *testing.T) {
	tests := []struct {
		in  string
		out bool
	}{
		{"8.8.8.8", true},
		{"2001:4860:4860::8888", true},
		{"::ffff:8.8.8.8", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fc00::1", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
	}

	for _, tt := range tests {
		if got := Routable(netip.MustParseAddr(tt.in)); got != tt.out {
			t.Errorf("Routable(%s) = %t, want %t", tt.in, got, tt.out)
		}
	}

	if Routable(netip.Addr{}) {
		t.Error("expected the zero address to be rejected")
	}
}

func TestLookupPortRejectsUnroutable(t *testing.T) {
	if err := LookupPort(netip.MustParseAddr("127.0.0.1"), 22); !errors.Is(err, ErrNotRoutable) {
		t.Errorf("expected ErrNotRoutable, got %v", err)
	}
}
