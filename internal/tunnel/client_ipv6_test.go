package tunnel

import "testing"

// A tunnel with no IPv6 address cannot carry IPv6, so the config generator
// decides purely on the interface address.
func TestHasIPv6Address(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		want    bool
	}{
		{"ipv4 only", "10.10.0.1/24", false},
		{"several ipv4", "10.10.0.1/24, 10.10.1.1/24", false},
		{"ipv6 only", "fd00::1/64", true},
		{"mixed", "10.10.0.1/24, fd00::1/64", true},
		{"empty", "", false},
		{"garbage", "not-an-address", false},
		{"bare ipv6 without mask", "fd00::1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasIPv6Address(tc.address); got != tc.want {
				t.Fatalf("hasIPv6Address(%q) = %v, want %v", tc.address, got, tc.want)
			}
		})
	}
}

func TestStripIPv6Routes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"ipv4 untouched", "0.0.0.0/0", "0.0.0.0/0"},
		{"drops default ipv6", "0.0.0.0/0, ::/0", "0.0.0.0/0"},
		{"drops leading ipv6", "::/0, 0.0.0.0/0", "0.0.0.0/0"},
		{"preserves order", "10.0.0.0/8, 192.168.0.0/16", "10.0.0.0/8, 192.168.0.0/16"},
		{"drops ipv6 between ipv4", "10.0.0.0/8, fd00::/8, 192.168.0.0/16", "10.0.0.0/8, 192.168.0.0/16"},
		{"tolerates spacing", "0.0.0.0/0 ,  ::/0 ", "0.0.0.0/0"},
		{"never empties the list", "::/0", "::/0"},
		{"empty stays empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripIPv6Routes(tc.input); got != tc.want {
				t.Fatalf("stripIPv6Routes(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
