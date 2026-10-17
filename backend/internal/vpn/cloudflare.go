package vpn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"strataos/internal/store"
)

type cfResp struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func cfCall(ctx context.Context, token, method, path string, body any) (cfResp, error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, CloudflareAPI+path, rd)
	if err != nil {
		return cfResp{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return cfResp{}, fmt.Errorf("Cloudflare unreachable")
	}
	defer resp.Body.Close()
	var r cfResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return r, fmt.Errorf("Cloudflare: HTTP %d", resp.StatusCode)
	}
	if !r.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(r.Errors) > 0 {
			msg = r.Errors[0].Message
		}
		return r, fmt.Errorf("Cloudflare: %s", msg)
	}
	return r, nil
}

// cloudflareUpdate creates or updates the A record of the host name.
func cloudflareUpdate(ctx context.Context, d store.DDNS, ip string) error {
	base := "/zones/" + d.ZoneID + "/dns_records"
	r, err := cfCall(ctx, d.Token, "GET", base+"?type=A&name="+url.QueryEscape(d.Hostname), nil)
	if err != nil {
		return err
	}
	var recs []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(r.Result, &recs)
	rec := map[string]any{"type": "A", "name": d.Hostname, "content": ip, "ttl": 300, "proxied": false}
	if len(recs) == 0 {
		_, err = cfCall(ctx, d.Token, "POST", base, rec)
		return err
	}
	if recs[0].Content == ip {
		return nil
	}
	_, err = cfCall(ctx, d.Token, "PUT", base+"/"+recs[0].ID, rec)
	return err
}
