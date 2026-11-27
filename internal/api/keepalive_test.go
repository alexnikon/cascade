package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestKeepaliveRangeRequestTypes(t *testing.T) {
	app := fiber.New()
	app.Post("/", func(c *fiber.Ctx) error {
		value, err := requestOptionalPeerString(c, "persistentKeepaliveV3")
		if err != nil {
			return err
		}
		if value == nil {
			return c.SendString("omitted")
		}
		return c.SendString("value:" + *value)
	})
	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{}`, 200, "omitted"}, {`{"persistentKeepaliveV3":""}`, 200, "value:"},
		{`{"persistentKeepaliveV3":"5-25"}`, 200, "value:5-25"},
		{`{"persistentKeepaliveV3":null}`, 400, "persistentKeepaliveV3"},
		{`{"persistentKeepaliveV3":25}`, 400, "persistentKeepaliveV3"},
		{`{"persistentKeepaliveV3":true}`, 400, "persistentKeepaliveV3"},
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if resp.StatusCode != tc.status || !strings.Contains(body, tc.want) {
			t.Fatalf("%s: status=%d body=%s", tc.body, resp.StatusCode, body)
		}
	}
}

func TestScalarKeepaliveCreateAndUpdate(t *testing.T) {
	app := fiber.New()
	api := app.Group("/api")
	RegisterPeers(api)
	RegisterInterfaces(api)
	base := "/api/tunnel-interfaces/" + wgIfaceID + "/peers"
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d body=%s err=%v", method, path, resp.StatusCode, data, err)
		}
		return data
	}
	for _, invalid := range []string{"-1", "65536", "1.5", "null", `"25"`} {
		request("POST", base, `{"name":"invalid","persistentKeepalive":`+invalid+"}", 400)
	}
	if os.Getenv("CASCADE_TEST_KEEPALIVE_API_RUNTIME") != "1" {
		return // Successful creation writes the real quick config in an isolated container.
	}

	global := peerDefaults().PersistentKeepalive
	for i, tc := range []struct {
		field string
		want  int
	}{
		{"", global}, {",\"persistentKeepalive\":0", 0}, {",\"persistentKeepalive\":65535", 65535},
	} {
		key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 21)}, 32))
		body := fmt.Sprintf(`{"name":"scalar-audit","publicKey":"%s","allowedIPs":"10.50.0.%d/32"%s}`, key, 20+i, tc.field)
		data := request("POST", base, body, 201)
		var result struct {
			Peer struct {
				ID                  string
				PersistentKeepalive int
			}
		}
		if err := json.Unmarshal(data, &result); err != nil || result.Peer.PersistentKeepalive != tc.want {
			t.Fatalf("create: got %s, want keepalive %d, err=%v", data, tc.want, err)
		}
		// Use distinct peer records in this shared fixture.
		t.Cleanup(func() { request("DELETE", base+"/"+result.Peer.ID, "", 204) })
		if tc.want == 0 {
			path := base + "/" + result.Peer.ID
			request("PATCH", path, `{"name":"modified","persistentKeepalive":1.5}`, 400)
			request("PUT", "/api/tunnel-interfaces/"+wgIfaceID+"/restore",
				`{"file":{"peers":[{"persistentKeepalive":65536}]}}`, 400)
			data := request("GET", path, "", 200)
			var saved struct {
				Name                string
				PersistentKeepalive int
			}
			if err := json.Unmarshal(data, &saved); err != nil || saved.Name != "scalar-audit" || saved.PersistentKeepalive != 0 {
				t.Fatalf("invalid update or restore changed peer: %s, %v", data, err)
			}
		}

	}
}
