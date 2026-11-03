package prometheusmetrics

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const Path = "/metrics"
const shutdownTimeout = 2 * time.Second

type runningServer struct {
	app      *fiber.App
	listener net.Listener
	address  string
}

// Server shares one collector between the VPN listener and the HTTPS backend.
// Reconciliation observes interface addresses without polling VPN tools.
type Server struct {
	mu           sync.Mutex
	manager      *Manager
	handler      fiber.Handler
	running      *runningServer
	listenErr    string
	resolveVPN   func(string) (string, error)
	proxyPeer    func() (netip.Addr, error)
	stop         chan struct{}
	done         chan struct{}
	shutdownOnce sync.Once
}

func NewServer(manager *Manager, collector prometheus.Collector) *Server {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	return &Server{manager: manager, handler: adaptor.HTTPHandler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{})), resolveVPN: manager.vpnAddress, proxyPeer: func() (netip.Addr, error) { return publicProxyPeer(os.Getenv("BIND_ADDR"), os.Getenv("NETWORK_MODE")) }}
}

// ConfigureProxy uses the actual parsed bind, including command-line overrides.
func (s *Server) ConfigureProxy(bind, network string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Capture the physical gateway before VPN routing starts. Later default-route
	// changes must not change which local proxy peer is trusted.
	peer, err := publicProxyPeer(bind, network)
	s.proxyPeer = func() (netip.Addr, error) { return peer, err }
}

func (s *Server) PublicSupported() error { _, err := s.proxyPeer(); return err }

func (s *Server) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	s.reconcileLocked()
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.mu.Lock()
				s.reconcileLocked()
				s.mu.Unlock()
			}
		}
	}()
}

func (s *Server) desiredAddress(config Snapshot) (string, error) {
	if !config.Enabled || config.Mode != ModeVPN {
		return "", nil
	}
	address, err := s.resolveVPN(config.InterfaceID)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(address, fmt.Sprint(config.Port)), nil
}

// Apply prepares the new listener before committing. Failed validation, binding
// or persistence leaves the previous configuration and listener intact.
func (s *Server) Apply(update Update) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.manager.next(update)
	if err != nil {
		return Snapshot{}, err
	}
	if next.Enabled && next.Mode == ModePublicHTTPS {
		if err := s.PublicSupported(); err != nil {
			return Snapshot{}, invalid(err.Error())
		}
	}
	address, err := s.desiredAddress(next)
	if err != nil {
		return Snapshot{}, invalid(err.Error())
	}
	var candidate net.Listener
	if address != "" && (s.running == nil || s.running.address != address) {
		candidate, err = net.Listen("tcp", address)
		if err != nil {
			return Snapshot{}, invalid("selected metrics address or port is unavailable")
		}
	}
	if err := s.manager.persist(next); err != nil {
		if candidate != nil {
			_ = candidate.Close()
		}
		return Snapshot{}, err
	}
	s.manager.publish(next)
	if address == "" {
		s.closeLocked()
	} else if candidate != nil {
		s.closeLocked()
		s.startLocked(candidate, address)
	}
	s.listenErr = ""
	return next, nil
}

func (s *Server) reconcileLocked() {
	address, err := s.desiredAddress(s.manager.Current())
	if err != nil {
		s.closeLocked()
		s.listenErr = err.Error()
		return
	}
	if address == "" {
		s.closeLocked()
		s.listenErr = ""
		return
	}
	if s.running != nil && s.running.address == address {
		return
	}
	s.closeLocked()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		s.listenErr = "selected metrics address or port is unavailable"
		return
	}
	s.startLocked(listener, address)
	s.listenErr = ""
}

func (s *Server) Status() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config := s.manager.Current()
	if config.Enabled && config.Mode == ModePublicHTTPS {
		if err := s.PublicSupported(); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	return s.running != nil, s.listenErr
}

func (s *Server) closeLocked() {
	if s.running != nil {
		_ = s.running.app.ShutdownWithTimeout(shutdownTimeout)
		_ = s.running.listener.Close()
		s.running = nil
	}
}

func (s *Server) Shutdown() error {
	s.mu.Lock()
	stop, done := s.stop, s.done
	if stop != nil {
		s.shutdownOnce.Do(func() { close(stop) })
	}
	s.closeLocked()
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}

func (s *Server) authorized(c *fiber.Ctx, config Snapshot, required bool) bool {
	if !required && !config.TokenConfigured {
		return true
	}
	auth := c.Get(fiber.HeaderAuthorization)
	hash := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
	return config.TokenConfigured && strings.HasPrefix(auth, "Bearer ") && subtle.ConstantTimeCompare(hash[:], config.tokenHash[:]) == 1
}

// PublicHandler is registered before the management access policy. It accepts
// only Caddy's dedicated HTTPS route, never UI sessions or API credentials.
func (s *Server) PublicHandler(c *fiber.Ctx) error {
	config := s.manager.Current()
	if !config.Enabled || config.Mode != ModePublicHTTPS {
		return c.SendStatus(fiber.StatusNotFound)
	}
	expected, err := s.proxyPeer()
	remote, ok := netip.AddrFromSlice(c.Context().RemoteIP())
	if err != nil || !ok || remote.Unmap() != expected || c.Get("X-Cascade-Metrics-Proxy") != "1" || c.Get("X-Cascade-HTTPS-Port") != "443" {
		return c.SendStatus(fiber.StatusNotFound)
	}
	if !s.authorized(c, config, true) {
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	return s.handler(c)
}

func (s *Server) startLocked(listener net.Listener, address string) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second})
	app.Get(Path, func(c *fiber.Ctx) error {
		config := s.manager.Current()
		current, err := s.desiredAddress(config)
		if err != nil || current != address || current == "" {
			return c.SendStatus(fiber.StatusNotFound)
		}
		if !s.authorized(c, config, false) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return s.handler(c)
	})
	s.running = &runningServer{app: app, listener: listener, address: address}
	go func() { _ = app.Listener(listener) }()
}
