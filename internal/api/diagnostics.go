// diagnostics.go — network diagnostics endpoints.
//
// Routes (all require auth):
//
//	POST /api/diagnostics/ping               ← ping a host, return reachable/latency/loss
//	GET  /api/diagnostics/ping/stream        ← SSE streaming ping with terminal-style output
//	GET  /api/diagnostics/traceroute/stream  ← SSE streaming traceroute
package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexnikon/cascade/internal/validate"
	"github.com/gofiber/fiber/v2"
)

func RegisterDiagnostics(api fiber.Router) {
	g := api.Group("/diagnostics")
	g.Post("/ping", diagnosticsPing)
	g.Get("/ping/stream", diagnosticsPingStream)
	g.Get("/traceroute/stream", diagnosticsTracerouteStream)
}

type PingRequest struct {
	Host      string `json:"host"`
	Count     int    `json:"count"`
	Interface string `json:"interface"`
}

type PingResult struct {
	Reachable  bool    `json:"reachable"`
	LatencyMs  float64 `json:"latencyMs"`
	PacketLoss int     `json:"packetLoss"` // percent
}

var (
	reLoss = regexp.MustCompile(`(\d+)% packet loss`)
	reRTT  = regexp.MustCompile(`(?:rtt|round-trip)[^\n]*=\s*[\d.]+/([\d.]+)/`)
)

func diagnosticsPing(c *fiber.Ctx) error {
	var req PingRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid JSON body")
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		return fiber.NewError(fiber.StatusBadRequest, "host is required")
	}
	for _, ch := range host {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '.' || ch == '-' || ch == ':' || ch == '[' || ch == ']' {
			continue
		}
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("invalid host: %q", host))
	}
	count := req.Count
	if count <= 0 || count > 10 {
		count = 3
	}

	if strings.HasPrefix(host, "-") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid host")
	}
	args := []string{"-c", strconv.Itoa(count), "-W", "2"}
	if req.Interface != "" {
		if err := validate.IfaceName(req.Interface); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		args = append(args, "-I", req.Interface)
	}
	args = append(args, host)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(count*3+2)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ping", args...)
	out, _ := cmd.Output() // ignore exit code — non-zero on packet loss

	result := parsePingOutput(string(out))
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 && result.PacketLoss == 100 {
		result.Reachable = false
	}

	if ctx.Err() != nil {
		return c.JSON(&PingResult{Reachable: false, PacketLoss: 100})
	}

	return c.JSON(result)
}

func parsePingOutput(out string) *PingResult {
	res := &PingResult{PacketLoss: 100}

	if m := reLoss.FindStringSubmatch(out); len(m) > 1 {
		loss, _ := strconv.Atoi(m[1])
		res.PacketLoss = loss
		res.Reachable = loss < 100
	}
	if m := reRTT.FindStringSubmatch(out); len(m) > 1 {
		avg, _ := strconv.ParseFloat(m[1], 64)
		res.LatencyMs = avg
	}
	return res
}

// diagnosticHost rejects command options and accepts IPv4, IPv6, and DNS names.
func diagnosticHost(c *fiber.Ctx) (string, error) {
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		return "", fiber.NewError(400, "host is required")
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if err := validate.HostOrIP(host); err != nil {
		return "", fiber.NewError(400, err.Error())
	}
	return strings.Clone(host), nil
}

func diagnosticSource(c *fiber.Ctx) (string, error) {
	source := strings.TrimSpace(c.Query("source"))
	if source != "" && net.ParseIP(source) == nil {
		if strings.HasPrefix(source, "-") {
			return "", fiber.NewError(400, "invalid source")
		}
		if err := validate.IfaceName(source); err != nil {
			return "", fiber.NewError(400, err.Error())
		}
	}
	return strings.Clone(source), nil
}

func diagnosticStreamHeaders(c *fiber.Ctx) {
	// Override the ordinary 30-second response deadline only for bounded utility
	// streams. Heartbeats still detect disconnects and commands have own limits.
	_ = c.Context().SetWriteDeadline(time.Now().Add(310 * time.Second))
	c.Response().ImmediateHeaderFlush = true
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-store")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
}

func diagnosticMessage(w *bufio.Writer, line string) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
		return err
	}
	return w.Flush()
}

// streamDiagnosticCommand drains stdout and stderr together. Heartbeats detect
// idle disconnected clients; cancellation gives commands time to flush on SIGINT.
func streamDiagnosticCommand(ctx context.Context, cancel context.CancelFunc, w *bufio.Writer, binary string, args []string) (result error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Second
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		cancel()
		return err
	}
	waited := make(chan error, 1)
	go func() { err := cmd.Wait(); writer.Close(); waited <- err }()
	lines := make(chan string)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		cancel()
		reader.Close()
		writer.Close()
		err := <-waited
		<-scanned
		if result == nil {
			result = err
		} else if !errors.Is(result, context.DeadlineExceeded) {
			result = context.Canceled
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if err := diagnosticMessage(w, line); err != nil {
				return err
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func diagnosticsPingStream(c *fiber.Ctx) error {
	host, err := diagnosticHost(c)
	if err != nil {
		return err
	}
	source, err := diagnosticSource(c)
	if err != nil {
		return err
	}
	count := 5
	if n, err := strconv.Atoi(c.Query("count")); err == nil && n >= 1 && n <= 20 {
		count = n
	}
	args := []string{"-c", strconv.Itoa(count), "-W", "2"}
	if source != "" {
		args = append(args, "-I", source)
	}
	if size := c.Query("size"); size != "" {
		n, err := strconv.Atoi(size)
		if err != nil || n < 0 || n > 65507 {
			return fiber.NewError(400, "invalid packet size")
		}
		args = append(args, "-s", strconv.Itoa(n))
	}
	if c.Query("df") == "true" {
		args = append(args, "-M", "do")
	}
	if tos := c.Query("tos"); tos != "" {
		n, err := strconv.Atoi(tos)
		if err != nil || n < 0 || n > 255 {
			return fiber.NewError(400, "invalid TOS")
		}
		args = append(args, "-Q", strconv.Itoa(n))
	}
	args = append(args, host)
	diagnosticStreamHeaders(c)
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(count*4+2)*time.Second)
		err := streamDiagnosticCommand(ctx, cancel, w, "ping", args)
		if err != nil {
			diagnosticMessage(w, "error: "+err.Error())
		}
		diagnosticMessage(w, "[done]")
	})
	return nil
}

func diagnosticsTracerouteStream(c *fiber.Ctx) error {
	host, err := diagnosticHost(c)
	if err != nil {
		return err
	}
	source, err := diagnosticSource(c)
	if err != nil {
		return err
	}
	args := []string{"-n", "-w", "1", "-q", "1", "-m", "30"}
	switch c.Query("type", "udp") {
	case "udp":
	case "icmp":
		args = append(args, "-I")
	case "tcp":
		args = append(args, "-T")
	default:
		return fiber.NewError(400, "invalid traceroute type")
	}
	if source != "" {
		if net.ParseIP(source) != nil {
			args = append(args, "-s", source)
		} else {
			args = append(args, "-i", source)
		}
	}
	args = append(args, host)
	diagnosticStreamHeaders(c)
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		err := streamDiagnosticCommand(ctx, cancel, w, "traceroute", args)
		if err != nil {
			diagnosticMessage(w, "error: "+err.Error())
		}
		diagnosticMessage(w, "[done]")
	})
	return nil
}
