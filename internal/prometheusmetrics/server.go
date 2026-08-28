package prometheusmetrics

import (
	"fmt"
	"log"
	"net"
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
	port     int
}

// Server owns the independent Prometheus listener and applies port changes
// without restarting Cascade's management API or VPN control plane.
type Server struct {
	mu        sync.Mutex
	manager   *Manager
	handler   fiber.Handler
	running   *runningServer
	listenErr string
}

func NewServer(manager *Manager, collector prometheus.Collector) *Server {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	return &Server{
		manager: manager,
		handler: adaptor.HTTPHandler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{})),
	}
}

// Start opens the persisted listener when enabled. Bind failures are reported
// through Status but do not prevent the rest of Cascade from starting.
func (s *Server) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	config := s.manager.Current()
	if !config.Enabled {
		return
	}
	listener, err := listen(config.Port)
	if err != nil {
		s.listenErr = "port is unavailable"
		log.Printf("metrics: cannot listen on 0.0.0.0:%d: %v", config.Port, err)
		return
	}
	s.startLocked(listener, config.Port)
}

// Apply validates, binds, persists, and publishes a complete settings update.
// When a new port cannot be opened, the old listener and snapshot stay active.
func (s *Server) Apply(update Update) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := s.manager.next(update)
	if err != nil {
		return Snapshot{}, err
	}

	var candidate net.Listener
	needsListener := next.Enabled && (s.running == nil || s.running.port != next.Port)
	if needsListener {
		candidate, err = listen(next.Port)
		if err != nil {
			return Snapshot{}, invalid(fmt.Sprintf("port %d is unavailable", next.Port))
		}
	}
	if err := s.manager.persist(next); err != nil {
		if candidate != nil {
			_ = candidate.Close()
		}
		return Snapshot{}, err
	}
	s.manager.publish(next)

	old := s.running
	if !next.Enabled {
		s.running = nil
		s.listenErr = ""
	} else if candidate != nil {
		s.startLocked(candidate, next.Port)
	} else {
		s.listenErr = ""
	}
	if old != nil && old != s.running {
		if err := old.app.ShutdownWithTimeout(shutdownTimeout); err != nil {
			log.Printf("metrics: shutdown old listener: %v", err)
		}
		_ = old.listener.Close()
	}
	return next, nil
}

func (s *Server) Status() (listening bool, listenError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running != nil, s.listenErr
}

func (s *Server) Shutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		return nil
	}
	running := s.running
	s.running = nil
	err := running.app.ShutdownWithTimeout(shutdownTimeout)
	_ = running.listener.Close()
	return err
}

func (s *Server) startLocked(listener net.Listener, port int) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get(Path, func(c *fiber.Ctx) error {
		config := s.manager.Current()
		if config.TokenConfigured {
			provided := strings.TrimPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
			if !s.manager.Authorize(provided) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
		}
		return s.handler(c)
	})
	running := &runningServer{app: app, listener: listener, port: port}
	s.running = running
	s.listenErr = ""
	go func() {
		if err := app.Listener(listener); err != nil {
			log.Printf("metrics: listener stopped: %v", err)
		}
	}()
}

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
}
