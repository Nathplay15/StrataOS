package davcal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

func server(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	h := &Handler{Dir: dir, Auth: func(w http.ResponseWriter, r *http.Request) (string, bool) {
		u, p, ok := r.BasicAuth()
		if !ok || p != "pw" || (u != "alice" && u != "bob") {
			w.Header().Set("WWW-Authenticate", `Basic realm="StrataOS"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return "", false
		}
		return u, true
	}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, dir
}

const event = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Europe/Paris\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:event-uid-1@example.com\r\nDTSTAMP:20261120T100000Z\r\nDTSTART;TZID=Europe/Paris:20261201T100000\r\nDTEND;TZID=Europe/Paris:20261201T110000\r\nSUMMARY:Dentist\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// A real client library (go-webdav's) discovers, writes, reads and queries.
func TestCalDAVWithClient(t *testing.T) {
	srv, _ := server(t)
	ctx := context.Background()
	hc := webdav.HTTPClientWithBasicAuth(srv.Client(), "alice", "pw")
	c, err := caldav.NewClient(hc, srv.URL+"/caldav/")
	if err != nil {
		t.Fatal(err)
	}
	wc, _ := webdav.NewClient(hc, srv.URL+"/caldav/")
	principal, err := wc.FindCurrentUserPrincipal(ctx)
	if err != nil || principal != "/caldav/alice/" {
		t.Fatalf("principal: %q %v", principal, err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil || len(cals) != 1 {
		t.Fatalf("calendars: %+v %v", cals, err)
	}
	cal, err := ical.NewDecoder(strings.NewReader(event)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	// A client-chosen name, unrelated to the UID.
	p := cals[0].Path + "0F3A9C2E-7B1D-4E55-9A0B-1234567890AB.ics"
	co, err := c.PutCalendarObject(ctx, p, cal)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GetCalendarObject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ETag == "" || got.ETag != co.ETag {
		t.Errorf("etag: %q vs %q", got.ETag, co.ETag)
	}
	var tz, ev int
	for _, ch := range got.Data.Children {
		switch ch.Name {
		case ical.CompTimezone:
			tz++
		case ical.CompEvent:
			ev++
		}
	}
	if tz != 1 || ev != 1 {
		t.Errorf("GET should return the VCALENDAR with its VTIMEZONE and VEVENT: %d %d", tz, ev)
	}
	objs, err := c.QueryCalendar(ctx, cals[0].Path, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", Props: []string{"SUMMARY", "UID"}}}},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT",
			Start: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)}}},
	})
	if err != nil || len(objs) != 1 || objs[0].Path != p {
		t.Fatalf("query: %+v %v", objs, err)
	}
	mg, err := c.MultiGetCalendar(ctx, cals[0].Path, &caldav.CalendarMultiGet{Paths: []string{p}})
	if err != nil || len(mg) != 1 {
		t.Fatalf("multiget: %+v %v", mg, err)
	}
	// The ETag follows the content, not its length.
	cal.Children[1].Props.SetText(ical.PropSummary, "Dentistx")
	co2, err := c.PutCalendarObject(ctx, p, cal)
	if err != nil || co2.ETag == co.ETag {
		t.Fatalf("etag should change: %q %q %v", co.ETag, co2.ETag, err)
	}
	// A stale If-Match is refused.
	req, _ := http.NewRequest("PUT", srv.URL+p, strings.NewReader(event))
	req.SetBasicAuth("alice", "pw")
	req.Header.Set("Content-Type", "text/calendar")
	req.Header.Set("If-Match", `"`+co.ETag+`"`)
	if res, err := srv.Client().Do(req); err != nil || res.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %v %v", res.StatusCode, err)
	}
	// Bob cannot read Alice's calendar.
	bob := webdav.HTTPClientWithBasicAuth(srv.Client(), "bob", "pw")
	cb, _ := caldav.NewClient(bob, srv.URL+"/caldav/")
	if _, err := cb.GetCalendarObject(ctx, p); err == nil {
		t.Error("bob read alice's event")
	}
	if err := wc.RemoveAll(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCalendarObject(ctx, p); err == nil {
		t.Error("deleted event still there")
	}
}

func TestPropfindNamespaces(t *testing.T) {
	srv, _ := server(t)
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/caldav/", strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`))
	req.SetBasicAuth("alice", "pw")
	req.Header.Set("Depth", "0")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusMultiStatus || !strings.Contains(string(b), `xmlns="DAV:"`) || !strings.Contains(string(b), "/caldav/alice/") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
}

func TestCardDAVWithClient(t *testing.T) {
	srv, _ := server(t)
	ctx := context.Background()
	hc := webdav.HTTPClientWithBasicAuth(srv.Client(), "alice", "pw")
	c, _ := carddav.NewClient(hc, srv.URL+"/carddav/")
	wc, _ := webdav.NewClient(hc, srv.URL+"/carddav/")
	principal, err := wc.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	books, err := c.FindAddressBooks(ctx, home)
	if err != nil || len(books) != 1 {
		t.Fatalf("books: %+v %v", books, err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "contact-1")
	card.SetValue(vcard.FieldFormattedName, "Ada Lovelace")
	card.SetValue(vcard.FieldName, "Lovelace;Ada;;;")
	p := books[0].Path + "random-name-42.vcf"
	if _, err := c.PutAddressObject(ctx, p, card); err != nil {
		t.Fatal(err)
	}
	objs, err := c.QueryAddressBook(ctx, books[0].Path, &carddav.AddressBookQuery{DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldFormattedName}}})
	if err != nil || len(objs) != 1 || objs[0].Card.PreferredValue(vcard.FieldFormattedName) != "Ada Lovelace" {
		t.Fatalf("query: %+v %v", objs, err)
	}
	got, err := c.GetAddressObject(ctx, p)
	if err != nil || got.Card.Value(vcard.FieldUID) != "contact-1" {
		t.Fatalf("get: %+v %v", got, err)
	}
}

func TestMigrationFromSingleFiles(t *testing.T) {
	srv, dir := server(t)
	os.MkdirAll(filepath.Join(dir, "alice"), 0o700)
	os.WriteFile(filepath.Join(dir, "alice", "calendar.ics"), []byte("BEGIN:VEVENT\nUID:old-1\nDTSTAMP:20260101T000000Z\nDTSTART:20260102T100000Z\nSUMMARY:Old\nEND:VEVENT\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "alice", "addressbook.vcf"), []byte("BEGIN:VCARD\nVERSION:3.0\nUID:c/1\nFN:Old Friend\nEND:VCARD\n"), 0o600)
	ctx := context.Background()
	hc := webdav.HTTPClientWithBasicAuth(srv.Client(), "alice", "pw")
	c, _ := caldav.NewClient(hc, srv.URL)
	co, err := c.GetCalendarObject(ctx, "/caldav/alice/calendars/default/old-1.ics")
	if err != nil || co.Data.Children[0].Props.Get(ical.PropUID).Value != "old-1" {
		t.Fatalf("migrated event: %+v %v", co, err)
	}
	cc, _ := carddav.NewClient(hc, srv.URL)
	if ao, err := cc.GetAddressObject(ctx, "/carddav/alice/addressbooks/default/c_1.vcf"); err != nil || ao.Card.PreferredValue(vcard.FieldFormattedName) != "Old Friend" {
		t.Fatalf("migrated card: %+v %v", ao, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alice", "calendar.ics.migrated")); err != nil {
		t.Error("old file not set aside")
	}
}
