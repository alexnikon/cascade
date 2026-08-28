package api

import (
	"errors"
	"github.com/gofiber/fiber/v2"

	"github.com/alexnikon/cascade/internal/prometheusmetrics"
)

type metricsSettingsResponse struct {
	Enabled                       bool   `json:"enabled"`
	Port                          int    `json:"port"`
	Path                          string `json:"path"`
	Listening                     bool   `json:"listening"`
	ListenError                   string `json:"listenError"`
	ConnectedPeerThresholdSeconds int    `json:"connectedPeerThresholdSeconds"`
	TokenConfigured               bool   `json:"tokenConfigured"`
	HistoryEnabled                bool   `json:"historyEnabled"`
	CanManage                     bool   `json:"canManage"`
}

// RegisterMetricsSettings exposes safe runtime metrics configuration. The
// write-only bearer token is never included in a response.
func RegisterMetricsSettings(api fiber.Router, manager *prometheusmetrics.Manager, server *prometheusmetrics.Server) {
	respond := func(c *fiber.Ctx, snapshot prometheusmetrics.Snapshot) error {
		listening, listenError := server.Status()
		return c.JSON(metricsSettingsResponse{
			Enabled: snapshot.Enabled, Port: snapshot.Port, Path: prometheusmetrics.Path,
			Listening: listening, ListenError: listenError,
			ConnectedPeerThresholdSeconds: snapshot.ConnectedPeerThresholdSeconds,
			TokenConfigured:               snapshot.TokenConfigured,
			HistoryEnabled:                snapshot.HistoryEnabled,
			CanManage:                     callerIsAdmin(c),
		})
	}

	api.Get("/settings/metrics", func(c *fiber.Ctx) error {
		return respond(c, manager.Current())
	})

	api.Put("/settings/metrics", func(c *fiber.Ctx) error {
		if !callerIsAdmin(c) {
			return fiber.NewError(fiber.StatusForbidden, "admin only")
		}
		var update prometheusmetrics.Update
		if err := c.BodyParser(&update); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid JSON body")
		}
		snapshot, err := server.Apply(update)
		if err != nil {
			var validationErr *prometheusmetrics.ValidationError
			if errors.As(err, &validationErr) {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, "failed to persist metrics settings")
		}
		return respond(c, snapshot)
	})
}
