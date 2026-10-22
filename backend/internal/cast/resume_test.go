package cast

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeCast is a Chromecast already playing a media for the Default Media
// Receiver, as found after a restart of the daemon.
func fakeCast(t *testing.T, content string) (addr string, got func() []string) {
	t.Helper()
	cert := httptest.NewUnstartedServer(nil)
	cert.StartTLS()
	t.Cleanup(cert.Close)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cert.TLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				state := "PAUSED"
				for {
					m, err := ReadMessage(conn)
					if err != nil {
						return
					}
					var p map[string]any
					_ = json.Unmarshal([]byte(m.Payload), &p)
					typ, _ := p["type"].(string)
					mu.Lock()
					seen = append(seen, m.Destination+":"+typ)
					mu.Unlock()
					reply := func(ns string, v map[string]any) {
						v["requestId"] = p["requestId"]
						b, _ := json.Marshal(v)
						_ = WriteMessage(conn, &Message{Source: m.Destination, Destination: m.Source, Namespace: ns, Payload: string(b)})
					}
					media := func() {
						reply(nsMedia, map[string]any{"type": "MEDIA_STATUS", "status": []any{map[string]any{
							"mediaSessionId": 3, "playerState": state, "currentTime": 12.5, "media": map[string]any{"contentId": content, "duration": 60.0}}}})
					}
					switch {
					case m.Namespace == nsReceiver && typ == "GET_STATUS":
						reply(nsReceiver, map[string]any{"type": "RECEIVER_STATUS", "status": map[string]any{
							"volume":       map[string]any{"level": 0.4},
							"applications": []any{map[string]any{"appId": DefaultMediaReceiver, "transportId": "t1"}}}})
					case m.Namespace == nsMedia && typ == "GET_STATUS":
						media()
					case m.Namespace == nsMedia && typ == "PLAY":
						if p["mediaSessionId"] == 3.0 {
							state = "PLAYING"
						}
						media()
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestResumeChromecast(t *testing.T) {
	const url = "http://192.168.1.2:7070/api/media/stream/TOKEN/film.mp4"
	addr, seen := fakeCast(t, url)
	ctx := context.Background()
	p, err := Resume(ctx, Receiver{Kind: "chromecast", Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	st := p.Status(ctx)
	if st.State != "PAUSED" || st.Position != 12.5 || st.Volume != 40 || st.Media != url {
		t.Fatalf("status after resume: %+v", st)
	}
	if Ended(st, "/stream/TOKEN/") {
		t.Fatal("the resumed media counted as ended")
	}
	// The controls reach the media session found on the device.
	if err := p.Play(ctx); err != nil {
		t.Fatal(err)
	}
	if st := p.Status(ctx); st.State != "PLAYING" {
		t.Fatalf("after play: %+v", st)
	}
	want := map[string]bool{"t1:CONNECT": false, "t1:PLAY": false}
	for _, s := range seen() {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("message %s not sent (got %v)", k, seen())
		}
	}
}

func TestEnded(t *testing.T) {
	const mark = "/stream/TOK/"
	for _, c := range []struct {
		st   Status
		want bool
	}{
		{Status{State: "PLAYING", Media: "http://nas:7070/api/media/stream/TOK/a.mp4"}, false},
		{Status{State: "PLAYING", Media: "http://other/film.mkv"}, true},
		{Status{State: "PAUSED"}, false},
		{Status{State: "STOPPED"}, false}, // stopped from the remote panel: the session stays
		{Status{State: "IDLE"}, true},     // nothing loaded any more
		{Status{State: "UNKNOWN"}, false}, // unreachable: kept until the link expires
		{Status{State: "ERROR"}, false},
	} {
		if got := Ended(c.st, mark); got != c.want {
			t.Errorf("Ended(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
}
