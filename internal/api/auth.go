// Browser authentication uses durable, revocable SQLite sessions. Pending login
// and 2FA setup challenges expire after ten minutes and never authorize API use.
package api

import (
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	totpLib "github.com/pquerna/otp/totp"

	"github.com/alexnikon/cascade/internal/sessions"
	"github.com/alexnikon/cascade/internal/tokens"
	"github.com/alexnikon/cascade/internal/users"
)

const localKeyTokenUserID = "token_user_id"
const localKeyBrowserSession = "browser_session"

// InitAuth cleans expired records without clearing valid sessions on restart.
func InitAuth() {
	if err := sessions.Cleanup(time.Now(), true); err != nil {
		log.Printf("auth: session cleanup: %v", err)
	}
}

// secureAuthTransport ignores untrusted Forwarded/X-Forwarded-Proto headers.
func secureAuthTransport(c *fiber.Ctx) bool {
	if c.Context().IsTLS() {
		return true
	}
	_, trusted := proxyClientIP(c)
	return trusted
}

func authCookieName(c *fiber.Ctx, name string) string {
	if secureAuthTransport(c) {
		return name + "_https"
	}
	return name
}

func setAuthCookie(c *fiber.Ctx, name, value string, expires time.Time) {
	c.Set("Cache-Control", "no-store")
	maxAge := int(time.Until(expires).Seconds())
	if value == "" {
		maxAge = -1
		expires = time.Unix(1, 0)
	}
	c.Cookie(&fiber.Cookie{Name: authCookieName(c, name), Value: value, Path: "/", HTTPOnly: true,
		Secure: secureAuthTransport(c), SameSite: "Strict", Expires: expires, MaxAge: maxAge})
}

func authIP(c *fiber.Ctx) string {
	if ip, ok := proxyClientIP(c); ok {
		return ip.String()
	}
	return c.Context().RemoteIP().String()
}

func browserSession(c *fiber.Ctx) (*sessions.Session, error) {
	if s, ok := c.Locals(localKeyBrowserSession).(*sessions.Session); ok {
		return s, nil
	}
	now := time.Now()
	if err := sessions.Cleanup(now, false); err != nil {
		return nil, err
	}
	token := c.Cookies(authCookieName(c, "session_id"))
	s, err := sessions.Lookup(token, now)
	if err != nil {
		return nil, err
	}
	renewed, err := sessions.Touch(s, authIP(c), now)
	if err != nil {
		return nil, err
	}
	if renewed {
		setAuthCookie(c, "session_id", token, s.ExpiresAt)
	}
	c.Locals(localKeyBrowserSession, s)
	return s, nil
}

func currentBrowserSessionID(c *fiber.Ctx) string {
	// A Bearer request never claims the browser cookie's identity or session.
	if id, _ := c.Locals(localKeyTokenUserID).(string); id != "" {
		return ""
	}
	if s, err := browserSession(c); err == nil {
		return s.ID
	}
	return ""
}

func authContext(c *fiber.Ctx) string {
	if id, _ := c.Locals(localKeyTokenUserID).(string); id != "" {
		return "token:" + sessions.Hash(c.Get("Authorization"))
	}
	if id := currentBrowserSessionID(c); id != "" {
		return "session:" + id
	}
	return ""
}

// AuthMiddleware preserves Bearer, legacy header and first-run open-mode access.
func AuthMiddleware(c *fiber.Ctx) error {
	if id, _ := c.Locals(localKeyTokenUserID).(string); id != "" {
		return c.Next()
	}
	n, err := users.Count()
	if err != nil {
		return fiber.NewError(500, "db error")
	}
	if n == 0 {
		return c.Next()
	}
	if hdr := c.Get("Authorization"); hdr != "" {
		if strings.HasPrefix(hdr, "Bearer ") {
			if id, err := tokens.VerifyAndTouch(strings.TrimPrefix(hdr, "Bearer ")); err == nil && id != "" {
				c.Locals(localKeyTokenUserID, id)
				return c.Next()
			}
		} else if checkAuthHeader(hdr) {
			return c.Next()
		}
	}
	if _, err := browserSession(c); err == nil {
		return c.Next()
	} else if !errors.Is(err, sessions.ErrInvalid) {
		return fiber.NewError(500, "session lookup failed")
	}
	c.Set("X-Cascade-Session-Expired", "1")
	return c.Status(401).JSON(fiber.Map{"error": "Unauthorized"})
}

func checkAuthHeader(hdr string) bool {
	if idx := strings.IndexByte(hdr, ':'); idx >= 0 {
		u, err := users.VerifyPassword(hdr[:idx], hdr[idx+1:])
		return err == nil && u != nil
	}
	u, err := users.VerifyPassword("admin", hdr)
	return err == nil && u != nil
}

func clearLogin(c *fiber.Ctx) error {
	if err := sessions.Logout(c.Cookies(authCookieName(c, "session_id"))); err != nil {
		return err
	}
	if err := sessions.DeleteChallenge(c.Cookies(authCookieName(c, "login_challenge"))); err != nil {
		return err
	}
	if err := sessions.DeleteChallenge(c.Cookies(authCookieName(c, "totp_setup"))); err != nil {
		return err
	}
	for _, name := range []string{"session_id", "login_challenge", "totp_setup"} {
		setAuthCookie(c, name, "", time.Time{})
	}
	c.Locals(localKeyBrowserSession, nil)
	return nil
}

func createBrowserSession(c *fiber.Ctx, userID string, version int64, challenge string) error {
	s, token, err := sessions.Create(userID, version, c.Get("User-Agent"), authIP(c), challenge, time.Now())
	if errors.Is(err, sessions.ErrInvalid) {
		return fiber.NewError(401, "Credentials changed. Please sign in again.")
	}
	if err != nil {
		return fiber.NewError(500, "session save error")
	}
	setAuthCookie(c, "session_id", token, s.ExpiresAt)
	setAuthCookie(c, "login_challenge", "", time.Time{})
	return c.JSON(fiber.Map{"authenticated": true})
}

// RegisterAuth retains the existing public login/logout/session-state contract.
func RegisterAuth(api fiber.Router) {
	api.Get("/session", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		n, err := users.Count()
		if err != nil {
			return fiber.NewError(500, "db error")
		}
		state := fiber.Map{"authenticated": n == 0, "requiresPassword": n != 0, "totp_pending": false, "username": ""}
		if n == 0 {
			return c.JSON(state)
		}
		if s, err := browserSession(c); err == nil {
			state["authenticated"], state["username"] = true, s.Username
		} else if !errors.Is(err, sessions.ErrInvalid) {
			return fiber.NewError(500, "session lookup failed")
		} else if ch, err := sessions.GetChallenge(c.Cookies(authCookieName(c, "login_challenge")), "login", "", time.Now()); err == nil {
			if u, err := users.GetByID(ch.UserID); err == nil && u != nil {
				state["totp_pending"], state["username"] = true, u.Username
			}
		}
		return c.JSON(state)
	})
	loginLimiter := limiter.New(limiter.Config{Max: 5, Expiration: time.Minute, KeyGenerator: authIP,
		LimitReached: func(c *fiber.Ctx) error {
			c.Set("Retry-After", "60")
			return c.Status(429).JSON(fiber.Map{"error": "Too many login attempts. Try again in 60 seconds."})
		}})
	api.Post("/session", loginLimiter, func(c *fiber.Ctx) error {
		n, err := users.Count()
		if err != nil {
			return fiber.NewError(500, "db error")
		}
		if n == 0 {
			return fiber.NewError(400, "No password is configured")
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Remember bool   `json:"remember"`
		}
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(400, "invalid JSON body")
		}
		if body.Username == "" {
			body.Username = "admin"
		}
		u, err := users.VerifyPassword(body.Username, body.Password)
		if err != nil || u == nil {
			time.Sleep(500 * time.Millisecond)
			return fiber.NewError(401, "Invalid credentials")
		}
		if err := clearLogin(c); err != nil {
			return fiber.NewError(500, "session logout error")
		}
		if u.TOTPEnabled {
			token, err := sessions.NewChallenge(u.ID, u.AuthVersion, "login", "", "", time.Now())
			if errors.Is(err, sessions.ErrInvalid) {
				return fiber.NewError(401, "Credentials changed. Please sign in again.")
			}
			if err != nil {
				return fiber.NewError(500, "session save error")
			}
			setAuthCookie(c, "login_challenge", token, time.Now().Add(sessions.ChallengeLifetime))
			return c.JSON(fiber.Map{"totp_required": true})
		}
		return createBrowserSession(c, u.ID, u.AuthVersion, "")
	})
	api.Delete("/session", func(c *fiber.Ctx) error {
		if err := clearLogin(c); err != nil {
			return fiber.NewError(500, "session logout error")
		}
		return c.JSON(fiber.Map{"authenticated": false})
	})
	totpLimiter := limiter.New(limiter.Config{Max: 5, Expiration: time.Minute, KeyGenerator: authIP})
	api.Post("/auth/totp/verify", totpLimiter, func(c *fiber.Ctx) error {
		raw := c.Cookies(authCookieName(c, "login_challenge"))
		ch, err := sessions.GetChallenge(raw, "login", "", time.Now())
		if errors.Is(err, sessions.ErrInvalid) {
			return fiber.NewError(401, "No pending TOTP session")
		}
		if err != nil {
			return fiber.NewError(500, "login challenge lookup failed")
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(400, "invalid JSON body")
		}
		secret, err := users.GetTOTPSecret(ch.UserID)
		if err != nil || secret == "" {
			return fiber.NewError(401, "TOTP not configured for this user")
		}
		if !totpLib.Validate(body.Code, secret) {
			return fiber.NewError(401, "Invalid TOTP code")
		}
		return createBrowserSession(c, ch.UserID, ch.AuthVersion, raw)
	})
}

func currentUserID(c *fiber.Ctx) (string, bool) {
	if id, _ := c.Locals(localKeyTokenUserID).(string); id != "" {
		return id, true
	}
	s, err := browserSession(c)
	if err != nil {
		return "", false
	}
	return s.UserID, true
}

func callerIsAdmin(c *fiber.Ctx) bool {
	if n, err := users.Count(); err == nil && n == 0 {
		return true
	}
	id, ok := currentUserID(c)
	if !ok {
		return false
	}
	admin, err := users.IsAdmin(id)
	return err == nil && admin
}

// RegisterSessions exposes browser authorizations separately from API tokens.
func RegisterSessions(api fiber.Router) {
	api.Get("/sessions", func(c *fiber.Ctx) error {
		id, ok := currentUserID(c)
		if !ok {
			return fiber.NewError(401, "not authenticated")
		}
		list, err := sessions.List(id, callerIsAdmin(c), currentBrowserSessionID(c), time.Now())
		if err != nil {
			return fiber.NewError(500, "session list failed")
		}
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"sessions": list})
	})
	api.Delete("/sessions/:id", func(c *fiber.Ctx) error {
		id, ok := currentUserID(c)
		if !ok {
			return fiber.NewError(401, "not authenticated")
		}
		current := c.Params("id") == currentBrowserSessionID(c)
		deleted, err := sessions.Revoke(c.Params("id"), id, callerIsAdmin(c), time.Now())
		if err != nil {
			return fiber.NewError(500, "session revoke failed")
		}
		if !deleted {
			return fiber.NewError(404, "session not found")
		}
		if current {
			if err := clearLogin(c); err != nil {
				return fiber.NewError(500, "session logout error")
			}
		}
		return c.JSON(fiber.Map{"ok": true, "current": current})
	})
}
