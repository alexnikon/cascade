package api

import (
	"net/netip"
	"os"
	"path"
	"strings"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/tokens"
	"github.com/alexnikon/cascade/internal/users"
	"github.com/alexnikon/cascade/internal/webaccess"
	"github.com/gofiber/fiber/v2"
)

// proxyClientIP never trusts a client-supplied forwarded-for chain. Caddy must
// overwrite these two headers, and the backend must only accept loopback peers.
func proxyClientIP(c *fiber.Ctx) (netip.Addr, bool) {
	peer, ok := netip.AddrFromSlice(c.Context().RemoteIP())
	if !ok || !peer.Unmap().IsLoopback() || c.Get("X-Cascade-Proxy") != webaccess.ProxyVersion || c.Get("X-Cascade-HTTPS-Port") != "443" {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(c.Get("X-Cascade-Client-IP"))
	return ip.Unmap(), err == nil && ip.Zone() == ""
}

func WebAccessMiddleware(manager *webaccess.Manager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		canonical := path.Clean(strings.ToLower(c.Path()))
		// Retired endpoints never fall through to the SPA, even in open mode.
		if canonical == "/cnf" || strings.HasPrefix(canonical, "/cnf/") || strings.HasSuffix(canonical, "/generateonetimelink") || canonical == "/api/wg-enable-one-time-links" {
			return c.SendStatus(fiber.StatusNotFound)
		}
		ip, trusted := proxyClientIP(c)
		class := manager.Classify(ip)
		if class == "public" {
			return c.Next()
		}
		if !trusted {
			return c.SendStatus(fiber.StatusNotFound)
		}
		switch class {
		case "admin":
			return c.Next()
		case "machine":
			path := canonical
			if !strings.HasPrefix(path, "/api/") || path == "/api/session" || strings.HasPrefix(path, "/api/auth/") {
				return c.SendStatus(fiber.StatusNotFound)
			}
			hdr := c.Get("Authorization")
			if !strings.HasPrefix(hdr, "Bearer ") {
				return c.SendStatus(fiber.StatusNotFound)
			}
			id, err := tokens.VerifyAndTouch(strings.TrimPrefix(hdr, "Bearer "))
			if err != nil || id == "" {
				return c.SendStatus(fiber.StatusNotFound)
			}
			c.Locals(localKeyTokenUserID, id)
			return c.Next()
		}
		return c.SendStatus(fiber.StatusNotFound)
	}
}

func RegisterWebAccess(r fiber.Router, manager *webaccess.Manager, bind string) {
	network := os.Getenv("NETWORK_MODE")
	if network == "" {
		network = "host"
	}
	supported := func(c *fiber.Ctx) bool {
		_, ok := proxyClientIP(c)
		return ok && webaccess.CheckRuntime(webaccess.Policy{Enabled: true}, bind, network) == nil
	}
	canManage := func(c *fiber.Ctx) bool {
		ip, _ := proxyClientIP(c)
		return callerIsAdmin(c) && manager.CanManageOrigin(ip)
	}
	respond := func(c *fiber.Ctx) error {
		interfaces, err := webaccess.Interfaces(db.DB())
		if err != nil {
			return fiber.NewError(500, err.Error())
		}
		return c.JSON(fiber.Map{"state": manager.State(), "interfaces": interfaces, "supported": supported(c), "canManage": canManage(c)})
	}
	r.Get("/settings/web-access", respond)
	r.Post("/settings/web-access/trial", func(c *fiber.Ctx) error {
		if !canManage(c) {
			return fiber.NewError(403, "administrative VPN access required")
		}
		if !supported(c) {
			return fiber.NewError(400, "requires updated Caddy, Linux host networking, and a loopback backend bind")
		}
		if count, err := users.Count(); err != nil || count == 0 {
			return fiber.NewError(400, "create an administrator account before restricting web access")
		}
		var p webaccess.Policy
		if err := c.BodyParser(&p); err != nil {
			return fiber.NewError(400, "invalid policy")
		}
		prepared, err := webaccess.Prepare(db.DB(), p)
		if err != nil {
			return fiber.NewError(400, err.Error())
		}
		if err = webaccess.CheckLive(prepared); err != nil {
			return fiber.NewError(400, err.Error())
		}
		if _, err = manager.Trial(p); err != nil {
			return fiber.NewError(400, err.Error())
		}
		return respond(c)
	})
	r.Post("/settings/web-access/confirm", func(c *fiber.Ctx) error {
		if !canManage(c) {
			return fiber.NewError(403, "admin only")
		}
		ip, ok := proxyClientIP(c)
		if !ok {
			return fiber.NewError(403, "trusted proxy required")
		}
		if _, err := manager.Confirm(ip); err != nil {
			return fiber.NewError(400, err.Error())
		}
		return respond(c)
	})
	r.Post("/settings/web-access/cancel", func(c *fiber.Ctx) error {
		if !canManage(c) {
			return fiber.NewError(403, "admin only")
		}
		if _, err := manager.Cancel(); err != nil {
			return fiber.NewError(500, err.Error())
		}
		return respond(c)
	})
}
