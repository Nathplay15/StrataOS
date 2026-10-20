package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudflareDNS01(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			io.WriteString(w, `{"success":false,"errors":[{"message":"bad token"}]}`)
			return
		}
		switch {
		case r.Method == "GET":
			io.WriteString(w, `{"success":true,"result":{"name":"Example.com"}}`)
		case r.Method == "POST":
			io.WriteString(w, `{"success":true,"result":{"id":"rec1"}}`)
		default:
			io.WriteString(w, `{"success":true,"result":{}}`)
		}
	}))
	defer srv.Close()
	old := CloudflareAPI
	CloudflareAPI = srv.URL
	defer func() { CloudflareAPI = old }()
	cf := &Cloudflare{Token: "tok", ZoneID: "z1"}
	ctx := context.Background()
	if !cf.Covers(ctx, "jellyfin.example.com") || !cf.Covers(ctx, "example.com") || cf.Covers(ctx, "example.com.evil.org") || cf.Covers(ctx, "notexample.com") {
		t.Error("zone matching")
	}
	cleanup, err := cf.Present(ctx, "_acme-challenge.jellyfin.example.com", "abc")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	var rec map[string]any
	post := calls[len(calls)-2]
	_ = json.Unmarshal([]byte(post[strings.Index(post, "{"):]), &rec)
	if !strings.HasPrefix(post, "POST /zones/z1/dns_records") || rec["type"] != "TXT" || rec["content"] != "abc" || rec["name"] != "_acme-challenge.jellyfin.example.com" {
		t.Errorf("create: %s", post)
	}
	if !strings.HasPrefix(calls[len(calls)-1], "DELETE /zones/z1/dns_records/rec1") {
		t.Errorf("cleanup: %s", calls[len(calls)-1])
	}
	bad := &Cloudflare{Token: "wrong", ZoneID: "z1"}
	if _, err := bad.Zone(ctx); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Errorf("error: %v", err)
	}
}
