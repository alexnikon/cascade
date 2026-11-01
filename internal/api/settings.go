// Package api contains all HTTP route handlers.
// Each file corresponds to one resource group (settings, interfaces, peers, …).
// Handlers are pure functions — no state, all state lives in internal/* packages.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/alexnikon/cascade/internal/awgcap"
	"github.com/alexnikon/cascade/internal/awgparams"
	"github.com/alexnikon/cascade/internal/firewall"
	"github.com/alexnikon/cascade/internal/settings"
)

// getHostname returns the real host hostname.
// Docker containers get a random hash from os.Hostname(), so we first try
// /host_hostname which is mounted from the host's /etc/hostname (read-only).
func getHostname() string {
	if b, err := os.ReadFile("/host_hostname"); err == nil {
		if h := strings.TrimSpace(string(b)); h != "" {
			return h
		}
	}
	h, _ := os.Hostname()
	return h
}

// awgRunMode returns "userspace" when amneziawg-go is active, "kernel" otherwise.
func awgRunMode() string {
	if os.Getenv("WG_QUICK_USERSPACE_IMPLEMENTATION") == "amneziawg-go" {
		return "userspace"
	}
	return "kernel"
}

// dockerNetworkMode returns the Docker network mode from the NETWORK_MODE env var.
// setup.sh writes NETWORK_MODE=host|bridge into deploy/.env which is passed to the container.
// Defaults to "host" if unset (historical default and most common deployment).
func dockerNetworkMode() string {
	switch os.Getenv("NETWORK_MODE") {
	case "bridge":
		return "bridge"
	case "none":
		return "none"
	default:
		return "host"
	}
}

// SettingsResponse wraps GlobalSettings and adds runtime-only fields
// (hostname, resolvedPublicIP, publicIPWarning, awgMode, networkMode) that are not stored in the DB.
type SettingsResponse struct {
	settings.GlobalSettings
	Hostname         string `json:"hostname"`
	ResolvedPublicIP string `json:"resolvedPublicIP"`
	PublicIPWarning  string `json:"publicIPWarning"`
	AwgMode          string `json:"awgMode"`     // "userspace" | "kernel"
	NetworkMode      string `json:"networkMode"` // "host" | "bridge" | "none"
	AWGEngineVersion string `json:"awgEngineVersion"`
	AWGToolsVersion  string `json:"awgToolsVersion"`
	AWGMaxProtocol   string `json:"awgMaxProtocol"`
	AWG3Supported    bool   `json:"awg3Supported"`
	AWG3SupportError string `json:"awg3SupportError"`
}

// RegisterSettings registers all /api/settings and /api/templates routes.
// Must be called after db.Init().
//
// Routes registered:
//
//	GET  /api/settings
//	PUT  /api/settings
//	GET  /api/templates
//	POST /api/templates
//	GET  /api/templates/:id
//	PUT  /api/templates/:id
//	DELETE /api/templates/:id
//	POST /api/templates/:id/set-default
//	POST /api/templates/:id/apply
//	POST /api/templates/generate
//	GET  /api/templates/generator-options
func RegisterSettings(api fiber.Router) {
	// ── Global Settings ───────────────────────────────────────────────────────

	// GET /api/settings — return current global settings + runtime info
	api.Get("/settings", func(c *fiber.Ctx) error {
		s, err := settings.GetSettings()
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		hostname := getHostname()
		resolvedIP, ipWarn := settings.ResolvePublicIP(s.PublicIPMode, s.PublicIPManual)
		capability := awgcap.Detect()
		return c.JSON(SettingsResponse{
			GlobalSettings:   *s,
			Hostname:         hostname,
			ResolvedPublicIP: resolvedIP,
			PublicIPWarning:  ipWarn,
			AwgMode:          awgRunMode(),
			NetworkMode:      dockerNetworkMode(),
			AWGEngineVersion: capability.EngineVersion,
			AWGToolsVersion:  capability.ToolsVersion,
			AWGMaxProtocol:   capability.MaxProtocol,
			AWG3Supported:    capability.AWG3Supported,
			AWG3SupportError: capability.SupportError,
		})
	})

	// PUT /api/settings — partial update
	// Body: { dns?, defaultPersistentKeepalive?, defaultClientAllowedIPs?,
	//         subnetPool?, portPool?,
	//         gatewayWindowSeconds?, gatewayHealthyThreshold?, gatewayDegradedThreshold?,
	//         routerName?, publicIPMode?, publicIPManual? }
	api.Put("/settings", func(c *fiber.Ctx) error {
		var body map[string]any
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid JSON body")
		}

		// Validate pool fields explicitly so the user gets a clear error message
		// instead of a silent revert to the previous (default) value.
		if v, ok := body["portPool"].(string); ok {
			if _, err := settings.ParsePortPool(v); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "portPool: "+err.Error())
			}
		}
		if v, ok := body["subnetPool"].(string); ok {
			ip, network, err := net.ParseCIDR(v)
			if err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "subnetPool: "+err.Error())
			}
			if !ip.Equal(network.IP) {
				return fiber.NewError(fiber.StatusBadRequest,
					"subnetPool: host bits are set — use a network address (e.g. 192.168.0.0/16)")
			}
		}
		// Validation is intentionally duplicated here (and in isValidSettingValue):
		// the handler must return 400 so the UI gets a clear error; isValidSettingValue
		// silently skips invalid values to protect existing DB state from corruption.
		if v, ok := body["defaultFwPolicy"].(string); ok {
			if v != "accept" && v != "drop" {
				return fiber.NewError(fiber.StatusBadRequest, "defaultFwPolicy: must be 'accept' or 'drop'")
			}
		}

		updated, err := settings.UpdateSettings(body)
		if err != nil {
			if errors.Is(err, settings.ErrInvalidClientRouting) {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		// Invalidate public IP cache if mode or manual IP changed.
		if _, ok := body["publicIPMode"]; ok {
			settings.InvalidateIPCache()
		}
		if _, ok := body["publicIPManual"]; ok {
			settings.InvalidateIPCache()
		}

		// Rebuild firewall chains immediately when default policy changes.
		if _, ok := body["defaultFwPolicy"]; ok {
			if fw := firewall.TryGet(); fw != nil {
				if err := fw.RebuildChains(); err != nil {
					log.Printf("settings: firewall RebuildChains after policy change: %v", err)
				}
			} else {
				// Firewall not yet initialized (e.g. during tests). Policy is persisted
				// in the DB and will be applied on the next RebuildChains call.
				log.Printf("settings: firewall not initialized — defaultFwPolicy persisted but kernel not updated yet")
			}
		}

		hostname := getHostname()
		resolvedIP, ipWarn := settings.ResolvePublicIP(updated.PublicIPMode, updated.PublicIPManual)
		capability := awgcap.Detect()

		log.Println("settings: updated")
		return c.JSON(SettingsResponse{
			GlobalSettings:   *updated,
			Hostname:         hostname,
			ResolvedPublicIP: resolvedIP,
			PublicIPWarning:  ipWarn,
			AwgMode:          awgRunMode(),
			NetworkMode:      dockerNetworkMode(),
			AWGEngineVersion: capability.EngineVersion,
			AWGToolsVersion:  capability.ToolsVersion,
			AWGMaxProtocol:   capability.MaxProtocol,
			AWG3Supported:    capability.AWG3Supported,
			AWG3SupportError: capability.SupportError,
		})
	})

	// ── AmneziaWG Templates ──────────────────────────────────────────────────

	// GET /api/templates — list all templates
	api.Get("/templates", func(c *fiber.Ctx) error {
		list, err := settings.GetTemplates()
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		// Return nil as [] not null (matches Node.js behaviour)
		if list == nil {
			list = []settings.Template{}
		}
		return c.JSON(fiber.Map{"templates": list})
	})

	// GET /api/templates/generator-options — pinned catalogues and version defaults.
	api.Get("/templates/generator-options", func(c *fiber.Ctx) error {
		options := awgparams.GeneratorOptions()
		options["serverSupportsTagC"] = awgRunMode() == "kernel"
		return c.JSON(options)
	})

	// POST /api/templates/generate — generate, optionally save, a versioned profile.
	api.Post("/templates/generate", func(c *fiber.Ctx) error {
		var body struct {
			awgparams.Options
			SaveName string `json:"saveName"`
		}
		if len(c.Body()) > 0 {
			if strings.TrimSpace(string(c.Body())) == "null" {
				return fiber.NewError(fiber.StatusBadRequest, "Invalid JSON body")
			}
			if err := json.Unmarshal(c.Body(), &body); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "Invalid JSON body")
			}
		}
		serverTagDisabled := body.UseTagC != nil && *body.UseTagC && awgRunMode() == "userspace"
		if serverTagDisabled {
			off := false
			body.UseTagC = &off
		}
		result, err := awgparams.GenerateProfile(body.Options)
		if err != nil {
			if errors.Is(err, awgparams.ErrInvalidOptions) {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		if serverTagDisabled {
			result.Warnings = append(result.Warnings, "The Cascade userspace engine does not support <c>; the tag was disabled for server compatibility.")
		}
		if strings.TrimSpace(body.SaveName) != "" {
			tmpl, err := createGeneratedTemplate(strings.TrimSpace(body.SaveName), result.Options.Host, result.ProtocolVersion, result.Params, result.MTU)
			if err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			return c.JSON(struct {
				*awgparams.Result
				Template *settings.Template `json:"template"`
			}{result, tmpl})
		}
		return c.JSON(result)
	})

	// POST /api/templates — create new template
	// Body: { name, isDefault?, jc, jmin, jmax, s1-s4, h1-h4, i1-i5 }
	api.Post("/templates", func(c *fiber.Ctx) error {
		var body settings.Template
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid JSON body")
		}
		if body.Name == "" {
			return fiber.NewError(fiber.StatusBadRequest, "Template name is required")
		}

		tmpl, err := settings.CreateTemplate(body)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		log.Printf("settings: template created %s (%s)", tmpl.ID, tmpl.Name)
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"template": tmpl})
	})

	// GET /api/templates/:id — get single template
	api.Get("/templates/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		tmpl, err := settings.GetTemplate(id)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		if tmpl == nil {
			return fiber.NewError(fiber.StatusNotFound, "Template not found")
		}
		return c.JSON(fiber.Map{"template": tmpl})
	})

	// PUT /api/templates/:id — update template (partial)
	api.Put("/templates/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")

		// Parse as map for partial update support
		var updates map[string]any
		if err := c.BodyParser(&updates); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid JSON body")
		}

		tmpl, err := settings.UpdateTemplate(id, updates)
		if err != nil {
			if err.Error() == "template not found" {
				return fiber.NewError(fiber.StatusNotFound, err.Error())
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		log.Printf("settings: template updated %s", id)
		return c.JSON(fiber.Map{"template": tmpl})
	})

	// DELETE /api/templates/:id — delete template
	api.Delete("/templates/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		if err := settings.DeleteTemplate(id); err != nil {
			if err.Error() == "template not found" {
				return fiber.NewError(fiber.StatusNotFound, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		log.Printf("settings: template deleted %s", id)
		return c.JSON(fiber.Map{"success": true})
	})

	// POST /api/templates/:id/set-default — mark template as default
	api.Post("/templates/:id/set-default", func(c *fiber.Ctx) error {
		id := c.Params("id")
		tmpl, err := settings.SetDefaultTemplate(id)
		if err != nil {
			if err.Error() == "template not found" {
				return fiber.NewError(fiber.StatusNotFound, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		log.Printf("settings: template set as default %s", id)
		return c.JSON(fiber.Map{"template": tmpl})
	})

	// POST /api/templates/:id/apply — get a copy of the versioned parameters.
	api.Post("/templates/:id/apply", func(c *fiber.Ctx) error {
		id := c.Params("id")
		params, err := settings.ApplyTemplate(id)
		if err != nil {
			if err.Error() == "template not found" {
				return fiber.NewError(fiber.StatusNotFound, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		return c.JSON(fiber.Map{"settings": params})
	})
}

func createGeneratedTemplate(name, host, version string, generated awgparams.Params, mtu int) (*settings.Template, error) {
	payload, err := json.Marshal(generated)
	if err != nil {
		return nil, err
	}
	var t settings.Template
	if err := json.Unmarshal(payload, &t); err != nil {
		return nil, err
	}
	t.Name = name
	t.Host = host
	t.ProtocolVersion = version
	t.MTU = mtu
	return settings.CreateTemplate(t)
}
