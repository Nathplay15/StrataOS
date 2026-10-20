package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CloudflareAPI is replaced in tests.
var CloudflareAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare solves DNS-01 challenges in a zone with the API token of the
// dynamic DNS settings (Zone DNS: edit).
type Cloudflare struct {
	Token, ZoneID string
	zone          string
}

var cfClient = &http.Client{Timeout: 30 * time.Second}

func (c *Cloudflare) call(ctx context.Context, method, path string, body any, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, CloudflareAPI+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cfClient.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare unreachable")
	}
	defer resp.Body.Close()
	var r struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("Cloudflare: HTTP %d", resp.StatusCode)
	}
	if !r.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(r.Errors) > 0 {
			msg = r.Errors[0].Message
		}
		return fmt.Errorf("Cloudflare: %s", msg)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// Zone returns the name of the zone (example.com).
func (c *Cloudflare) Zone(ctx context.Context) (string, error) {
	if c.zone != "" {
		return c.zone, nil
	}
	var z struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "GET", "/zones/"+c.ZoneID, nil, &z); err != nil {
		return "", err
	}
	c.zone = strings.ToLower(z.Name)
	return c.zone, nil
}

// Covers reports whether host is in the zone.
func (c *Cloudflare) Covers(ctx context.Context, host string) bool {
	z, err := c.Zone(ctx)
	return err == nil && z != "" && (host == z || strings.HasSuffix(host, "."+z))
}

// Present creates the TXT record; cleanup deletes it.
func (c *Cloudflare) Present(ctx context.Context, name, value string) (func(), error) {
	var rec struct {
		ID string `json:"id"`
	}
	base := "/zones/" + c.ZoneID + "/dns_records"
	if err := c.call(ctx, "POST", base, map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60}, &rec); err != nil {
		return func() {}, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.call(ctx, "DELETE", base+"/"+rec.ID, nil, nil)
	}, nil
}
