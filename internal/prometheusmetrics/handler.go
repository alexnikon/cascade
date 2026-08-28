package prometheusmetrics

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Register installs the optional unauthenticated scrape endpoint. When Token is
// set, scrapes must use Authorization: Bearer <token>.
func Register(app *fiber.App, manager *Manager, collector prometheus.Collector) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	handler := adaptor.HTTPHandler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	app.Use(func(c *fiber.Ctx) error {
		config := manager.Current()
		if !config.Enabled || c.Method() != fiber.MethodGet || c.Path() != config.Path {
			return c.Next()
		}
		if config.TokenConfigured {
			provided := strings.TrimPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
			if !manager.Authorize(provided) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
		}
		return handler(c)
	})
}
