package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/market"
	"strataos/internal/store"
)

func marketTestServer(t *testing.T) (*Server, func(method, path, body string) (int, map[string]any)) {
	t.Helper()
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		return nil
	})
	s.marketRoutes()
	tok := s.sessions.create("admin", "admin", "192.0.2.10", time.Hour)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.10:5555"
		req.Header.Set("X-Strata", "1")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			var list []any
			if json.Unmarshal(w.Body.Bytes(), &list) == nil {
				out = map[string]any{"list": list}
			}
		}
		return w.Code, out
	}
	return s, call
}

type catalogServer struct {
	mu    sync.Mutex
	index []byte
	sig   []byte
}

func (c *catalogServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/catalog.json":
		w.Write(c.index)
	case "/catalog.json.sig":
		if c.sig == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(c.sig)
	default:
		http.NotFound(w, r)
	}
}

func catalogJSON(version string) []byte {
	return []byte(`{"format":1,"name":"Test apps","apps":[
{"id":"whoami","name":"Whoami","version":"` + version + `","maintainer":"t","webPort":"WEB_PORT",
 "vars":[{"key":"WEB_PORT","label":"Web port","default":"8099","type":"port"}],
 "compose":"services:\n  w:\n    image: traefik/whoami\n    ports: [\"${WEB_PORT}:80\"]\n"},
{"id":"sock","name":"Socket","version":"1","maintainer":"t",
 "compose":"services:\n  s:\n    image: x\n    privileged: true\n    volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]\n"},
{"id":"evil","name":"Evil","version":"1","maintainer":"t","compose":"services:\n  e:\n    image: x\n    env_file: /etc/shadow\n"}]}`)
}

func TestCatalogSourcesPinned(t *testing.T) {
	_, call := marketTestServer(t)
	cs := &catalogServer{index: catalogJSON("1.0")}
	srv := httptest.NewServer(cs)
	defer srv.Close()
	u := srv.URL + "/catalog.json"

	code, res := call("POST", "/api/apps/sources", `{"url":"`+u+`"}`)
	conf, _ := res["confirm"].(map[string]any)
	if code != 200 || conf == nil || conf["apps"].(float64) != 2 || len(conf["invalid"].([]any)) != 1 {
		t.Fatalf("first add should ask to confirm: %d %v", code, res)
	}
	sha := conf["sha256"].(string)
	if code, _ := call("GET", "/api/apps/sources", ""); code != 200 {
		t.Fatal(code)
	}
	if _, res := call("GET", "/api/apps/sources", ""); len(res["list"].([]any)) != 0 {
		t.Fatal("source saved before confirmation")
	}
	if code, res := call("POST", "/api/apps/sources", `{"url":"`+u+`","sha256":"`+strings.Repeat("0", 64)+`"}`); code != 400 || !strings.Contains(res["error"].(string), "checksum") {
		t.Errorf("wrong checksum: %d %v", code, res)
	}
	code, res = call("POST", "/api/apps/sources", `{"url":"`+u+`","sha256":"`+sha+`"}`)
	if code != 200 || res["trust"] != "pinned" || res["apps"].(float64) != 2 {
		t.Fatalf("pinned add: %d %v", code, res)
	}
	id := res["id"].(string)
	if code, _ := call("POST", "/api/apps/sources", `{"url":"`+u+`","sha256":"`+sha+`"}`); code != 400 {
		t.Error("same catalog added twice")
	}

	// What the apps get.
	code, res = call("POST", "/api/apps/community/"+id+"/whoami/check", `{"values":{"WEB_PORT":"8100"}}`)
	if code != 200 || res["grants"].(map[string]any)["ports"].([]any)[0].(map[string]any)["published"] != "8100" {
		t.Fatalf("check: %d %v", code, res)
	}
	if req := res["required"].(map[string]any); len(req) != 0 {
		t.Errorf("whoami needs approvals: %v", req)
	}
	code, res = call("POST", "/api/apps/community/"+id+"/sock/check", `{}`)
	req, _ := res["required"].(map[string]any)
	if code != 200 || req["privileged"] != true || req["hostPaths"].([]any)[0] != "/var/run/docker.sock" || res["grants"].(map[string]any)["dockerSocket"] != true {
		t.Errorf("risky app: %d %v", code, res)
	}
	if code, _ := call("POST", "/api/apps/community/"+id+"/evil/check", `{}`); code != 404 {
		t.Errorf("invalid app offered: %d", code)
	}
	if code, res := call("POST", "/api/apps/community/"+id+"/whoami/check", `{"values":{"WEB_PORT":"99999"}}`); code != 400 {
		t.Errorf("bad port: %d %v", code, res)
	}

	// The catalog changes: the new index waits for approval.
	cs.mu.Lock()
	cs.index = catalogJSON("2.0")
	cs.mu.Unlock()
	code, res = call("POST", "/api/apps/sources/"+id+"/refresh", "")
	pending, _ := res["pending"].(string)
	if code != 200 || pending == "" || pending == sha {
		t.Fatalf("refresh: %d %v", code, res)
	}
	_, res = call("GET", "/api/apps/community", "")
	if v := res["apps"].([]any)[1].(map[string]any)["version"]; v != "1.0" {
		t.Errorf("unaccepted version offered: %v", v)
	}
	if code, _ := call("POST", "/api/apps/sources/"+id+"/accept", `{"sha256":"`+sha+`"}`); code != 400 {
		t.Error("accepted another checksum")
	}
	if code, res := call("POST", "/api/apps/sources/"+id+"/accept", `{"sha256":"`+pending+`"}`); code != 200 || res["sha256"] != pending || res["pending"] != nil {
		t.Fatalf("accept: %d %v", code, res)
	}
	_, res = call("GET", "/api/apps/community", "")
	if v := res["apps"].([]any)[1].(map[string]any)["version"]; v != "2.0" {
		t.Errorf("accepted version not offered: %v", v)
	}

	if code, _ := call("DELETE", "/api/apps/sources/"+id, ""); code != 200 {
		t.Fatal(code)
	}
	if _, res := call("GET", "/api/apps/community", ""); len(res["apps"].([]any)) != 0 || len(res["sources"].([]any)) != 0 {
		t.Errorf("source still listed: %v", res)
	}
}

func TestCatalogSourcesSigned(t *testing.T) {
	_, call := marketTestServer(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	idx := catalogJSON("1.0")
	cs := &catalogServer{index: idx, sig: []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, idx)))}
	srv := httptest.NewServer(cs)
	defer srv.Close()
	u := srv.URL + "/catalog.json"
	key := market.FormatPublicKey(pub)
	other, _, _ := ed25519.GenerateKey(rand.Reader)

	if code, res := call("POST", "/api/apps/sources", `{"url":"`+u+`","publicKey":"`+market.FormatPublicKey(other)+`"}`); code != 400 || !strings.Contains(res["error"].(string), "signature") {
		t.Errorf("wrong key accepted: %d %v", code, res)
	}
	code, res := call("POST", "/api/apps/sources", `{"url":"`+u+`","publicKey":"`+key+`"}`)
	if code != 200 || res["trust"] != "signed" {
		t.Fatalf("signed add: %d %v", code, res)
	}
	id := res["id"].(string)

	// A new signed version replaces the old one at once.
	idx2 := catalogJSON("2.0")
	cs.mu.Lock()
	cs.index, cs.sig = idx2, ed25519.Sign(priv, idx2)
	cs.mu.Unlock()
	if code, res := call("POST", "/api/apps/sources/"+id+"/refresh", ""); code != 200 || res["pending"] != nil || res["lastError"] != nil {
		t.Fatalf("signed refresh: %d %v", code, res)
	}
	// A tampered index is refused and the last good one kept.
	cs.mu.Lock()
	cs.index = catalogJSON("6.6.6")
	cs.mu.Unlock()
	code, res = call("POST", "/api/apps/sources/"+id+"/refresh", "")
	if code != 400 || !strings.Contains(res["error"].(string), "signature") {
		t.Errorf("tampered refresh: %d %v", code, res)
	}
	_, res = call("GET", "/api/apps/community", "")
	src := res["sources"].([]any)[0].(map[string]any)
	if v := res["apps"].([]any)[1].(map[string]any)["version"]; v != "2.0" || !strings.Contains(src["lastError"].(string), "signature") {
		t.Errorf("after a tampered refresh: version %v, source %v", v, src)
	}
}

func TestCatalogSourceAddress(t *testing.T) {
	_, call := marketTestServer(t)
	for _, u := range []string{"http://example.org/catalog.json", "file:///etc/passwd", "git+ssh://x/y"} {
		if code, _ := call("POST", "/api/apps/sources", `{"url":"`+u+`"}`); code != 400 {
			t.Errorf("%s: %d", u, code)
		}
	}
}
