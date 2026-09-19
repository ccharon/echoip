package iputil

import (
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

func TestPublic(t *testing.T) {
	tests := []struct {
		in  string
		out bool
	}{
		{"1.2.3.4", true},
		{"203.0.114.1", true},
		{"2606:4700:4700::1111", true},
		// Transition mechanisms stay reachable, so they stay public.
		{"2002:0a00:0001::1", true},
		{"64:ff9b::808:808", true},
		{"2001::1", true},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"10.0.0.5", false},
		{"100.64.1.1", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"172.16.0.1", false},
		{"192.0.0.1", false},
		{"192.0.2.1", false},
		{"192.168.1.1", false},
		{"198.18.0.1", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"224.0.0.1", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		{"::", false},
		{"::1", false},
		{"100::1", false},
		{"2001:2::1", false},
		{"2001:db8::1", false},
		{"3fff::1", false},
		{"5f00::1", false},
		{"fc00::1", false},
		{"fd00::1", false},
		{"fe80::1", false},
		{"ff02::1", false},
	}
	for _, tt := range tests {
		addr := netip.MustParseAddr(tt.in).Unmap()
		if got := Public(addr); got != tt.out {
			t.Errorf("Public(%s) = %t, want %t", tt.in, got, tt.out)
		}
	}

	if Public(netip.Addr{}) {
		t.Error("the zero address is not public")
	}
	if Public(netip.MustParseAddr("2606:4700::1%eth0")) {
		t.Error("an address with a zone is not public")
	}
	// An IPv4 address inside IPv6 is judged by the address it carries.
	if Public(netip.MustParseAddr("::ffff:10.0.0.5").Unmap()) {
		t.Error("a mapped private address is not public")
	}
}
