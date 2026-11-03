package api

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/gofiber/fiber/v2"

	"github.com/alexnikon/cascade/internal/prometheusmetrics"
)

type metricsSettingsResponse struct {
	Enabled                       bool                          `json:"enabled"`
	Mode                          string                        `json:"mode"`
	InterfaceID                   string                        `json:"interfaceId"`
	Interfaces                    []prometheusmetrics.Interface `json:"interfaces"`
	EndpointURL                   string                        `json:"endpointUrl"`
	PublicURL                     string                        `json:"publicUrl"`
	PublicSupported               bool                          `json:"publicSupported"`
	PublicError                   string                        `json:"publicError"`
	ConfigurationRequired         bool                          `json:"configurationRequired"`
	Port                          int                           `json:"port"`
	Path                          string                        `json:"path"`
	Listening                     bool                          `json:"listening"`
	ListenError                   string                        `json:"listenError"`
	ConnectedPeerThresholdSeconds int                           `json:"connectedPeerThresholdSeconds"`
	TokenConfigured               bool                          `json:"tokenConfigured"`
	HistoryEnabled                bool                          `json:"historyEnabled"`
	CanManage                     bool                          `json:"canManage"`
}

// RegisterMetricsSettings exposes safe runtime metrics configuration. The
// write-only bearer token is never included in a response.
func RegisterMetricsSettings(api fiber.Router, manager *prometheusmetrics.Manager, server *prometheusmetrics.Server) {
	respond := func(c *fiber.Ctx, snapshot prometheusmetrics.Snapshot) error {
		listening, listenError := server.Status()
		interfaces, err := manager.Interfaces()
		if err != nil {
			return fiber.NewError(500, "failed to load metrics interfaces")
		}
		host := os.Getenv("WG_HOST")
		if host == "" {
			host = c.Hostname()
		}
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			host = "[" + host + "]"
		}
		publicURL := (&url.URL{Scheme: "https", Host: host, Path: prometheusmetrics.Path}).String()
		endpoint := ""
		if snapshot.Mode == prometheusmetrics.ModePublicHTTPS {
			endpoint = publicURL
		}
		if snapshot.Mode == prometheusmetrics.ModeVPN {
			if address, err := manager.InterfaceAddress(snapshot.InterfaceID); err == nil {
				endpoint = "http://" + net.JoinHostPort(address, fmt.Sprint(snapshot.Port)) + prometheusmetrics.Path
			}
		}
		publicError := ""
		if err := server.PublicSupported(); err != nil {
			publicError = err.Error()
		}
		return c.JSON(metricsSettingsResponse{
			Mode: snapshot.Mode, InterfaceID: snapshot.InterfaceID, Interfaces: interfaces,
			EndpointURL: endpoint, PublicURL: publicURL, PublicSupported: publicError == "", PublicError: publicError, ConfigurationRequired: snapshot.ConfigurationRequired,
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
