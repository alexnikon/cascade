package api

import (
	"errors"
	"log"

	"github.com/gofiber/fiber/v2"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/dnsalias"
)

func RegisterDomainAliasDNSSettings(api fiber.Router) {
	type response struct {
		dnsalias.UpstreamStatus
		CanManage bool `json:"canManage"`
	}
	api.Get("/settings/domain-alias-dns", func(c *fiber.Ctx) error {
		resolver := dnsalias.Get()
		if resolver == nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "Domain alias resolver is unavailable")
		}
		return c.JSON(response{resolver.Upstreams(), callerIsAdmin(c)})
	})
	api.Put("/settings/domain-alias-dns", func(c *fiber.Ctx) error {
		if !callerIsAdmin(c) {
			return fiber.NewError(fiber.StatusForbidden, "admin only")
		}
		resolver := dnsalias.Get()
		if resolver == nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "Domain alias resolver is unavailable")
		}
		var config dnsalias.UpstreamConfig
		if err := c.BodyParser(&config); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid JSON body")
		}
		status, err := resolver.UpdateUpstreams(db.DB(), config)
		if errors.Is(err, dnsalias.ErrInvalidUpstreams) {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		if err != nil {
			log.Printf("dnsalias: save upstreams: %v", err)
			return fiber.NewError(fiber.StatusInternalServerError, "Unable to save domain alias DNS settings")
		}
		return c.JSON(response{status, true})
	})
}
