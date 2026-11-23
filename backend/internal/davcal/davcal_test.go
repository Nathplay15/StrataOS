package davcal

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const event = "BEGIN:VEVENT\nUID:evt-1\nSUMMARY:Stand-up\nDTSTART:20261124T090000Z\nEND:VEVENT\n"
const event2 = "BEGIN:VEVENT\nUID:evt-1\nSUMMARY:Stand-up (moved)\nDTSTART:20261124T100000Z\nEND:VEVENT\n"
const event3 = "BEGIN:VEVENT\nUID:evt-2\nSUMMARY:Review\nDTSTART:20261125T140000Z\nEND:VEVENT\n"

func testHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
		Dir:  t.TempDir(),
		Auth: func(w http.ResponseWriter, r *http.Request) (string, bool) { return "alice", true },
	}
}

func do(t *testing.T, h *Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "https://nas.example"+path, rd)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCalDAVFlow(t *testing.T) {
	h := testHandler(t)

	// Discovery: the principal exposes the home sets.
	w := do(t, h, "PROPFIND", "/principals/alice/", "", map[string]string{"Depth": "0"})
	if w.Code != 207 || !strings.Contains(w.Body.String(), "/caldav/alice/default/") || !strings.Contains(w.Body.String(), "/carddav/alice/default/") {
		t.Fatalf("principal discovery: %d %s", w.Code, w.Body.String())
	}

	// PUT an event, then another.
	if w := do(t, h, http.MethodPut, "/caldav/alice/default/evt-1.ics", event, nil); w.Code != 201 {
		t.Fatalf("put evt-1: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, h, http.MethodPut, "/caldav/alice/default/evt-2.ics", event3, nil); w.Code != 201 {
		t.Fatalf("put evt-2: %d", w.Code)
	}
	// PUT the same UID again with new content: replaced, not duplicated.
	if w := do(t, h, http.MethodPut, "/caldav/alice/default/evt-1.ics", event2, nil); w.Code != 201 {
		t.Fatalf("put evt-1 again: %d", w.Code)
	}
	data, _ := readFile(t, h.file("alice", "caldav"))
	if got := SortedUIDs(string(data), "VEVENT"); len(got) != 2 || got[0] != "evt-1" || got[1] != "evt-2" {
		t.Fatalf("uids = %v, want [evt-1 evt-2]", got)
	}
	if !strings.Contains(string(data), "Stand-up (moved)") || strings.Contains(string(data), "SUMMARY:Stand-up\n") {
		t.Fatal("evt-1 must be replaced, not duplicated")
	}

	// PROPFIND the collection lists both resources.
	w = do(t, h, "PROPFIND", "/caldav/alice/default/", "", map[string]string{"Depth": "1"})
	if w.Code != 207 || !strings.Contains(w.Body.String(), "evt-1.ics") || !strings.Contains(w.Body.String(), "evt-2.ics") {
		t.Fatalf("propfind collection: %d %s", w.Code, w.Body.String())
	}

	// REPORT returns the calendar data.
	w = do(t, h, "REPORT", "/caldav/alice/default/", `<?xml version="1.0"?><C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop></C:calendar-query>`, nil)
	if w.Code != 207 || !strings.Contains(w.Body.String(), "BEGIN:VEVENT") || !strings.Contains(w.Body.String(), "Stand-up (moved)") {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}

	// GET one event.
	w = do(t, h, http.MethodGet, "/caldav/alice/default/evt-2.ics", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "SUMMARY:Review") {
		t.Fatalf("get evt-2: %d %s", w.Code, w.Body.String())
	}

	// DELETE it.
	if w := do(t, h, http.MethodDelete, "/caldav/alice/default/evt-2.ics", "", nil); w.Code != 204 {
		t.Fatalf("delete evt-2: %d", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/caldav/alice/default/evt-2.ics", "", nil); w.Code != 404 {
		t.Fatalf("get deleted: %d", w.Code)
	}
}

func TestCardDAVFlow(t *testing.T) {
	h := testHandler(t)
	vcf := "BEGIN:VCARD\nVERSION:3.0\nUID:card-1\nFN:Alice\nEND:VCARD\n"
	if w := do(t, h, http.MethodPut, "/carddav/alice/default/card-1.vcf", vcf, nil); w.Code != 201 {
		t.Fatalf("put vcard: %d %s", w.Code, w.Body.String())
	}
	w := do(t, h, "REPORT", "/carddav/alice/default/", `<?xml version="1.0"?><C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><C:addressbook-data/></D:prop></C:addressbook-query>`, nil)
	if w.Code != 207 || !strings.Contains(w.Body.String(), "FN:Alice") {
		t.Fatalf("carddav report: %d %s", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodGet, "/carddav/alice/default/card-1.vcf", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "BEGIN:VCARD") {
		t.Fatalf("get vcard: %d %s", w.Code, w.Body.String())
	}
}

func TestAuthRequired(t *testing.T) {
	h := &Handler{Dir: t.TempDir(), Auth: func(w http.ResponseWriter, r *http.Request) (string, bool) {
		w.Header().Set("WWW-Authenticate", `Basic realm="StrataOS"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return "", false
	}}
	w := do(t, h, http.MethodGet, "/caldav/alice/default/x.ics", "", nil)
	if w.Code != 401 {
		t.Fatalf("no auth: %d", w.Code)
	}
}

func readFile(t *testing.T, p string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(p)
}
