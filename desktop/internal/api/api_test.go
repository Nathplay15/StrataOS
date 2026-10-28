package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPinning(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer stdev_x" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"authentication required"}`))
			return
		}
		w.Write([]byte(`{"hostname":"nas","version":"0.8.0","api":1}`))
	}))
	defer srv.Close()
	fp := Fingerprint(srv.Certificate().Raw)
	got, trusted, err := ProbeCertificate(context.Background(), srv.URL)
	if err != nil || got != fp || trusted {
		t.Fatalf("probe: %v %q %v", err, got, trusted)
	}
	// First use, no pin: refused with the fingerprint to show.
	_, err = New(srv.URL, "stdev_x", "").Info(context.Background())
	var ce *CertError
	if !errors.As(err, &ce) || ce.Presented != fp || ce.Pinned != "" {
		t.Fatalf("unpinned: %v", err)
	}
	// Pinned: accepted, whatever the host name.
	if info, err := New(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), "stdev_x", fp).Info(context.Background()); err != nil || info.Hostname != "nas" {
		t.Fatalf("pinned: %v %+v", err, info)
	}
	// Another pin: the certificate "changed".
	other := NormalizeFingerprint(strings.Repeat("AB", 32))
	if _, err := New(srv.URL, "stdev_x", other).Info(context.Background()); !errors.As(err, &ce) || ce.Pinned != other {
		t.Fatalf("changed certificate: %v", err)
	}
	// Errors carry the status.
	if _, err := New(srv.URL, "stdev_bad", fp).Info(context.Background()); StatusOf(err) != 401 || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("401: %v", err)
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	hex := strings.Repeat("0a", 32)
	want := strings.TrimSuffix(strings.Repeat("0A:", 32), ":")
	for _, in := range []string{hex, strings.ToUpper(hex), want, strings.ReplaceAll(want, ":", " ")} {
		if NormalizeFingerprint(in) != want {
			t.Errorf("%q", in)
		}
	}
	for _, in := range []string{"", "zz", hex + "00"} {
		if NormalizeFingerprint(in) != "" {
			t.Errorf("%q accepted", in)
		}
	}
}
