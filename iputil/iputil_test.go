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
