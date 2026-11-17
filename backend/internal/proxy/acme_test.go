package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeACME answers just enough of RFC 8555 to see the order the client
// places, then refuses it.
type fakeACME struct {
	mu          sync.Mutex
	identifiers []string
}

func (f *fakeACME) handler(t *testing.T) http.Handler {
	var srvURL string
	mux := http.NewServeMux()
	nonce := func(w http.ResponseWriter) { w.Header().Set("Replay-Nonce", fmt.Sprintf("n%d", time.Now().UnixNano())) }
	mux.HandleFunc("/dir", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce": srvURL + "/nonce", "newAccount": srvURL + "/acct", "newOrder": srvURL + "/order",
		})
	})
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, r *http.Request) { nonce(w) })
	mux.HandleFunc("/acct", func(w http.ResponseWriter, r *http.Request) {
		nonce(w)
		w.Header().Set("Location", srvURL+"/acct/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		nonce(w)
		var jws struct {
			Payload string `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&jws)
		b, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
		var p struct {
			Identifiers []struct{ Value string } `json:"identifiers"`
		}
		_ = json.Unmarshal(b, &p)
		f.mu.Lock()
		for _, id := range p.Identifiers {
			f.identifiers = append(f.identifiers, id.Value)
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:malformed","detail":"test stops here"}`))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if srvURL == "" {
			srvURL = "http://" + r.Host
		}
		mux.ServeHTTP(w, r)
	})
}

type nopDNS struct{}

func (nopDNS) Present(context.Context, string, string) (func(), error) { return func() {}, nil }

// The order of a wildcard certificate carries every name of the certificate
// (review 0.12.0: only the main name was ordered, so Let's Encrypt refused
// the CSR and the wildcard was never issued).
func TestObtainOrdersEveryName(t *testing.T) {
	f := &fakeACME{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	cs, err := NewCertStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs.Directory = srv.URL + "/dir"
	err = cs.Obtain(context.Background(), "nas.example.com", "", nopDNS{}, func(string, ...any) {}, "*.nas.example.com")
	if err == nil || !strings.Contains(err.Error(), "test stops here") {
		t.Fatalf("obtain: %v", err)
	}
	if got := strings.Join(f.identifiers, ","); got != "nas.example.com,*.nas.example.com" {
		t.Fatalf("ordered %q", got)
	}
	// A wildcard without DNS-01 is refused before anything is asked.
	if err := cs.Obtain(context.Background(), "nas.example.com", "", nil, func(string, ...any) {}, "*.nas.example.com"); err == nil || !strings.Contains(err.Error(), "DNS-01") {
		t.Fatalf("wildcard over HTTP-01: %v", err)
	}
}

// challDNS publishes TXT records through pebble-challtestsrv.
type challDNS struct{ mgmt string }

func (c challDNS) Present(ctx context.Context, name, value string) (func(), error) {
	post := func(path string, body any) error {
		b, _ := json.Marshal(body)
		resp, err := http.Post(c.mgmt+path, "application/json", bytes.NewReader(b))
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("%s: %d", path, resp.StatusCode)
		}
		return nil
	}
	// challtestsrv keeps several values per name: both the name and its
	// wildcard are validated with the same TXT name.
	if err := post("/set-txt", map[string]string{"host": name + ".", "value": value}); err != nil {
		return func() {}, err
	}
	return func() { _ = post("/clear-txt", map[string]string{"host": name + "."}) }, nil
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Against Pebble (Let's Encrypt's test server), when STRATA_PEBBLE points to
// a folder holding the pebble and pebble-challtestsrv binaries and
// STRATA_PEBBLE_SRC to pebble's source tree (its test certificates):
//
//	GOBIN=$PWD/bin go install github.com/letsencrypt/pebble/v2/cmd/pebble@v2.8.0 \
//	    github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@v2.8.0
//	STRATA_PEBBLE=$PWD/bin STRATA_PEBBLE_SRC=$(go env GOMODCACHE)/github.com/letsencrypt/pebble/v2@v2.8.0 \
//	    go test ./internal/proxy -run Pebble
func TestObtainWildcardPebble(t *testing.T) {
	bin, src := os.Getenv("STRATA_PEBBLE"), os.Getenv("STRATA_PEBBLE_SRC")
	if bin == "" || src == "" {
		t.Skip("STRATA_PEBBLE and STRATA_PEBBLE_SRC not set")
	}
	dnsPort, mgmtPort, acmePort, pebMgmt := freePort(t), freePort(t), freePort(t), freePort(t)
	chall := exec.Command(filepath.Join(bin, "pebble-challtestsrv"), "-dns01", fmt.Sprintf("127.0.0.1:%d", dnsPort),
		"-management", fmt.Sprintf("127.0.0.1:%d", mgmtPort), "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")
	if err := chall.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = chall.Process.Kill(); _ = chall.Wait() }()
	cfg := filepath.Join(t.TempDir(), "pebble.json")
	conf := map[string]any{"pebble": map[string]any{
		"listenAddress": fmt.Sprintf("127.0.0.1:%d", acmePort), "managementListenAddress": fmt.Sprintf("127.0.0.1:%d", pebMgmt),
		"certificate": filepath.Join(src, "test/certs/localhost/cert.pem"), "privateKey": filepath.Join(src, "test/certs/localhost/key.pem"),
		"httpPort": 5002, "tlsPort": 5001,
	}}
	b, _ := json.Marshal(conf)
	if err := os.WriteFile(cfg, b, 0o600); err != nil {
		t.Fatal(err)
	}
	peb := exec.Command(filepath.Join(bin, "pebble"), "-config", cfg, "-dnsserver", fmt.Sprintf("127.0.0.1:%d", dnsPort))
	peb.Env = append(os.Environ(), "PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0", "PEBBLE_AUTHZREUSE=0")
	if err := peb.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peb.Process.Kill(); _ = peb.Wait() }()

	roots := x509.NewCertPool()
	ca, err := os.ReadFile(filepath.Join(src, "test/certs/pebble.minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots.AppendCertsFromPEM(ca)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}, Timeout: 30 * time.Second}
	dir := fmt.Sprintf("https://127.0.0.1:%d/dir", acmePort)
	for i := 0; ; i++ {
		resp, err := client.Get(dir)
		if err == nil {
			resp.Body.Close()
			break
		}
		if i > 50 {
			t.Fatalf("pebble not up: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	cs, err := NewCertStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs.Directory, cs.HTTPClient, cs.Propagation = dir, client, time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := cs.Obtain(ctx, "nas.example.test", "", challDNS{fmt.Sprintf("http://127.0.0.1:%d", mgmtPort)}, t.Logf, "*.nas.example.test"); err != nil {
		t.Fatalf("obtain: %v", err)
	}
	cert, err := cs.GetWildcard("nas.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if names := cert.Leaf.DNSNames; !slices.Contains(names, "nas.example.test") || !slices.Contains(names, "*.nas.example.test") {
		t.Fatalf("names %v", names)
	}
	if cs.NeedsWildcard("nas.example.test") {
		t.Fatal("a fresh wildcard must not need renewal")
	}
}
