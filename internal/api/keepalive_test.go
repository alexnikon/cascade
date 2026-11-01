package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestKeepaliveRangeRequestTypes(t *testing.T) {
	app := fiber.New()
	app.Post("/", func(c *fiber.Ctx) error {
		value, err := requestKeepaliveRange(c)
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
