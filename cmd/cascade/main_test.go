package main

import "testing"

func TestFrontendStaticPaths(t *testing.T) {
	tests := []struct {
		path   string
		static bool
	}{
		{path: "/", static: false},
		{path: "/api/session", static: false},
		{path: "/manifest.json", static: true},
		{path: "/sw.js", static: true},
		{path: "/js/pwa.js", static: true},
		{path: "/img/pwa-icon-512.png", static: true},
	}

	for _, test := range tests {
		if got := isFrontendStaticPath(test.path); got != test.static {
			t.Errorf("isFrontendStaticPath(%q) = %t, want %t", test.path, got, test.static)
		}
	}
}
