package main

import (
	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
	"testing"
)

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

type unreadStream struct{}

func (unreadStream) Read([]byte) (int, error) { panic("metrics consumed a response stream") }

func TestResponseMetricSizePreservesStreams(t *testing.T) {
	app := fiber.New()
	ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(ctx)
	ctx.Response().SetBodyString("plain response")
	if size := responseMetricSize(ctx); size != len("plain response") {
		t.Fatalf("buffered size %d", size)
	}
	ctx.Response().SetBodyStream(unreadStream{}, -1)
	if size := responseMetricSize(ctx); size != 0 {
		t.Fatalf("stream size %d", size)
	}
	ctx.Response().SetBodyStream(unreadStream{}, 128)
	if size := responseMetricSize(ctx); size != 128 {
		t.Fatalf("known stream size %d", size)
	}
	if !ctx.Response().IsBodyStream() {
		t.Fatal("stream was buffered")
	}
}
