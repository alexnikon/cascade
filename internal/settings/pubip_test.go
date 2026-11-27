package settings

import "testing"

func TestResolveAutoIPv4(t *testing.T) {
	for _, tc := range []struct {
		name        string
		replies     []string
		route, want string
		wantWarning bool
	}{
		{"skip nonpublic and non-IPv4 replies", []string{"2001:db8::1", "::ffff:8.8.8.8", "10.0.0.1", "invalid", "8.8.8.8"}, "", "8.8.8.8", false},
		{"private IPv4 fallback", []string{"2001:db8::1"}, "192.168.1.2", "192.168.1.2", true},
		{"public IPv4 fallback", nil, "1.1.1.1", "1.1.1.1", false},
		{"IPv6-only", []string{"2001:db8::1"}, "2001:db8::2", "", true},
		{"no route", nil, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := 0
			got, warning := resolveAutoIPv4(func(string) string {
				if index == len(tc.replies) {
					return ""
				}
				reply := tc.replies[index]
				index++
				return reply
			}, func() string { return tc.route })
			if got != tc.want || (warning != "") != tc.wantWarning {
				t.Errorf("got %q (%q), want %q (warning=%v)", got, warning, tc.want, tc.wantWarning)
			}
		})
	}
	if got, warning := ResolvePublicIP("manual", "2001:db8::1"); got != "2001:db8::1" || warning != "" {
		t.Fatalf("manual IPv6: %q (%q)", got, warning)
	}
}
