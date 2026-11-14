package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// The helper is an actual interruptible child process, without requiring raw
// socket privileges on developer machines. Real packets are tested in the VM.
func TestDiagnosticsHelperProcess(t *testing.T) {
	if os.Getenv("CASCADE_DIAGNOSTICS_HELPER") != "1" {
		return
	}
	index := 0
	for index < len(os.Args) && os.Args[index] != "--" {
		index++
	}
	args := os.Args[index+2:]
	encoded, _ := json.Marshal(args)
	fmt.Println("args: " + string(encoded))
	fmt.Fprintln(os.Stderr, "tool stderr")
	if strings.Contains(strings.Join(args, " "), "hold.test") {
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		<-interrupts
		os.WriteFile(os.Getenv("CASCADE_DIAGNOSTICS_STOPPED"), []byte("stopped"), 0600)
		os.Exit(0)
	}
	fmt.Println("3 packets transmitted, 3 received, 0% packet loss")
	os.Exit(0)
}

func diagnosticTools(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CASCADE_DIAGNOSTICS_HELPER", "1")
	t.Setenv("CASCADE_DIAGNOSTICS_BINARY", os.Args[0])
	for _, name := range []string{"ping", "traceroute"} {
		script := "#!/bin/sh\nexec \"$CASCADE_DIAGNOSTICS_BINARY\" -test.run=TestDiagnosticsHelperProcess -- " + name + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestDiagnosticsStreamArgumentsAndValidation(t *testing.T) {
	diagnosticTools(t)
	app := fiber.New()
	app.Get("/ping", diagnosticsPingStream)
	app.Get("/trace", diagnosticsTracerouteStream)
	for _, tc := range []struct {
		path     string
		expected []string
	}{
		{"/ping?host=127.0.0.1&source=lo&count=3&size=64&tos=16&df=true", []string{"-I", "lo", "-s", "64", "-Q", "16", "-M", "do"}},
		{"/trace?host=127.0.0.1&source=lo&type=tcp", []string{"-i", "lo", "-T"}},
		{"/trace?host=127.0.0.1&source=127.0.0.1&type=icmp", []string{"-s", "127.0.0.1", "-I"}},
		{"/trace?host=%5B%3A%3A1%5D&type=udp", []string{"::1"}},
	} {
		resp, err := app.Test(httptest.NewRequest("GET", tc.path, nil), 5000)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "data: [done]") || !strings.Contains(string(body), "tool stderr") {
			t.Fatalf("stream: %d %s", resp.StatusCode, body)
		}
		for _, arg := range tc.expected {
			encoded, _ := json.Marshal(arg)
			if !bytes.Contains(body, encoded) {
				t.Fatalf("missing %s in %s", arg, body)
			}
		}
	}
	for _, path := range []string{"/ping?host=-V", "/trace?host=-V", "/ping?host=127.0.0.1&source=-lo", "/trace?host=127.0.0.1&source=lo%3Bid", "/trace?host=127.0.0.1&type=other", "/ping?host=127.0.0.1&size=999999", "/ping?host=127.0.0.1&tos=256"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("invalid request accepted: %s => %d", path, resp.StatusCode)
		}
	}
}

func TestDiagnosticsPingOutputFormats(t *testing.T) {
	for _, summary := range []string{"rtt min/avg/max/mdev = 1.0/2.5/3.0/0.1 ms", "round-trip min/avg/max = 1.0/2.5/3.0 ms"} {
		result := parsePingOutput("3 packets transmitted, 3 received, 0% packet loss\n" + summary)
		if !result.Reachable || result.LatencyMs != 2.5 {
			t.Fatalf("ping parse %+v", result)
		}
	}
}

func TestDiagnosticsDisconnectCancelsIdleProcess(t *testing.T) {
	diagnosticTools(t)
	marker := filepath.Join(t.TempDir(), "stopped")
	t.Setenv("CASCADE_DIAGNOSTICS_STOPPED", marker)
	app := fiber.New(fiber.Config{WriteTimeout: 50 * time.Millisecond})
	app.Get("/ping", diagnosticsPingStream)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Listener(listener)
	t.Cleanup(func() { app.ShutdownWithTimeout(3 * time.Second) })
	client := &http.Client{Timeout: 7 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/ping?host=hold.test")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	// Receiving a heartbeat also proves the utility deadline supersedes the
	// normal short response deadline for this route only.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		if strings.Contains(line, "keep-alive") {
			break
		}
	}
	response.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("disconnected utility process did not receive SIGINT")
}
