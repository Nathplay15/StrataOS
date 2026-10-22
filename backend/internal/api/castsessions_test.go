package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos/internal/cast"
	"strataos/internal/media"
)

// fakeRenderer answers the AVTransport actions with the URI it plays.
func fakeRenderer(t *testing.T, uri *string, mu *sync.Mutex, actions *[]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		a := strings.Trim(r.Header.Get("SOAPAction"), `"`)
		name := a[strings.Index(a, "#")+1:]
		mu.Lock()
		*actions = append(*actions, name)
		u := *uri
		mu.Unlock()
		body := ""
		switch name {
		case "GetTransportInfo":
			body = "<CurrentTransportState>PLAYING</CurrentTransportState>"
		case "GetPositionInfo":
			body = "<RelTime>0:00:05</RelTime><TrackDuration>0:01:00</TrackDuration><TrackURI>" + u + "</TrackURI>"
		}
		w.Write([]byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:` + name + `Response xmlns:u="x">` + body + `</u:` + name + `Response></s:Body></s:Envelope>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCastSessionsSurviveRestart(t *testing.T) {
	s := newTestServer(t)
	t.Cleanup(func() {
		castState.Lock()
		castState.sessions = map[string]*castSession{}
		castState.Unlock()
	})
	var mu sync.Mutex
	var actions []string
	key := s.mediaSecret()
	now := time.Now()
	tokA := media.SignStream(key, "aaaa", "admin", "/srv/strata/main/Films/a.mp4", now.Add(streamTTL))
	urlA := "http://10.0.0.2:7070/api/media/stream/" + tokA + "/a.mp4"
	uriA, uriB := urlA, "http://10.0.0.9/other.mp4" // B's TV now plays something else
	ra := fakeRenderer(t, &uriA, &mu, &actions)
	rb := fakeRenderer(t, &uriB, &mu, &actions)
	recv := func(id, base string) cast.Receiver {
		return cast.Receiver{ID: id, Kind: "dlna", Name: id, Addr: "10.0.0.3", AVTransport: base + "/av", RenderingControl: base + "/rc", Video: true}
	}
	castState.Lock()
	castState.sessions = map[string]*castSession{
		"aaaa": {ID: "aaaa", User: "admin", Path: "/srv/strata/main/Films/a.mp4", Name: "a.mp4", Kind: "video", Started: now, ready: true,
			receivers: []*castReceiver{{Receiver: recv("dlna:a", ra.URL), url: urlA, resume: true, pos: 5}}},
		"bbbb": {ID: "bbbb", User: "admin", Name: "b.mp4", Kind: "video", Started: now, ready: true,
			receivers: []*castReceiver{{Receiver: recv("dlna:b", rb.URL), url: "http://10.0.0.2:7070/api/media/stream/tokB/b.mp4", resume: true}}},
		"old": {ID: "old", User: "admin", Name: "c.mp4", Kind: "video", Started: now.Add(-streamTTL - time.Minute), ready: true,
			receivers: []*castReceiver{{Receiver: recv("dlna:a", ra.URL), url: urlA, resume: true}}},
		"loading": {ID: "loading", User: "admin", Name: "d.mp4", Started: now, receivers: []*castReceiver{{Receiver: recv("dlna:a", ra.URL), resume: true}}},
	}
	castState.Unlock()
	s.saveCasts()

	// Restart: the memory is empty, the key and the sessions are read back.
	castState.Lock()
	castState.sessions = map[string]*castSession{}
	castState.Unlock()
	mediaKeyMu.Lock()
	mediaKey = nil
	mediaKeyMu.Unlock()
	s.restoreCasts()
	if !s.castSessionActive("aaaa") {
		t.Fatal("stream of the restored session refused before the check")
	}
	if g, err := media.VerifyStream(s.mediaSecret(), tokA, time.Now()); err != nil || g.Session != "aaaa" {
		t.Fatalf("token no longer valid after the restart: %v %v", g, err)
	}
	if s.castSessionActive("old") || s.castSessionActive("loading") {
		t.Fatal("expired or unfinished session restored")
	}
	for i := 0; i < 100; i++ {
		castState.Lock()
		a, b := castState.sessions["aaaa"], castState.sessions["bbbb"]
		done := a != nil && a.ready && b == nil
		castState.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	castState.Lock()
	a, b := castState.sessions["aaaa"], castState.sessions["bbbb"]
	castState.Unlock()
	if a == nil || !a.ready || b != nil {
		t.Fatalf("after the check: a=%v b=%v (b ended while down)", a, b)
	}
	rc := a.receivers[0]
	if rc.AVTransport != ra.URL+"/av" || rc.url != urlA {
		t.Fatalf("receiver not restored: %+v", rc.Receiver)
	}
	p := rc.use(t.Context())
	if p == nil {
		t.Fatal("no player for the restored receiver")
	}
	if err := p.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := actions[len(actions)-1]
	mu.Unlock()
	if last != "Pause" {
		t.Fatalf("control not sent to the renderer: %v", actions)
	}
	// The file no longer holds the ended session.
	castState.Lock()
	castState.sessions = map[string]*castSession{}
	castState.Unlock()
	s.restoreCasts()
	castState.Lock()
	_, okB := castState.sessions["bbbb"]
	_, okA := castState.sessions["aaaa"]
	castState.Unlock()
	if okB || !okA {
		t.Fatalf("saved file after the check: a=%v b=%v", okA, okB)
	}
}

func TestStreamMark(t *testing.T) {
	if got := streamMark("http://10.0.0.2:7070/api/media/stream/abc.def/film%20x.mp4"); got != "/api/media/stream/abc.def/" {
		t.Errorf("streamMark = %q", got)
	}
}
