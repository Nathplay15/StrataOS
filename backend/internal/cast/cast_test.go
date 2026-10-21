package cast

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestCastMessageRoundTrip(t *testing.T) {
	m := &Message{Source: "sender-0", Destination: "receiver-0", Namespace: nsReceiver, Payload: `{"type":"GET_STATUS","requestId":1}`}
	b := m.Marshal()
	// Known encoding: field 1 varint 0, then field 2 "sender-0".
	if !bytes.HasPrefix(b, []byte{0x08, 0x00, 0x12, 0x08, 's', 'e', 'n', 'd', 'e', 'r', '-', '0'}) {
		t.Fatalf("unexpected encoding % x", b[:12])
	}
	got, err := Unmarshal(b)
	if err != nil || got.Source != m.Source || got.Destination != m.Destination || got.Namespace != m.Namespace || got.Payload != m.Payload || got.Binary != nil {
		t.Fatalf("got %+v %v", got, err)
	}

	var buf bytes.Buffer
	long := &Message{Source: "a", Destination: "b", Namespace: nsMedia, Payload: strings.Repeat("x", 300)} // multi-byte varint length
	if err := WriteMessage(&buf, long); err != nil {
		t.Fatal(err)
	}
	if n := int(buf.Bytes()[0])<<24 | int(buf.Bytes()[1])<<16 | int(buf.Bytes()[2])<<8 | int(buf.Bytes()[3]); n != buf.Len()-4 {
		t.Fatalf("length prefix %d, body %d", n, buf.Len()-4)
	}
	r, err := ReadMessage(&buf)
	if err != nil || r.Payload != long.Payload {
		t.Fatalf("read back: %v", err)
	}

	bin := &Message{Source: "a", Destination: "b", Namespace: "urn:x-cast:com.google.cast.tp.deviceauth", Binary: []byte{1, 2, 3}}
	if got, err := Unmarshal(bin.Marshal()); err != nil || !bytes.Equal(got.Binary, []byte{1, 2, 3}) {
		t.Fatalf("binary: %+v %v", got, err)
	}
}

func TestCastMessageUnknownAndBroken(t *testing.T) {
	b := (&Message{Source: "s", Destination: "d", Namespace: "n", Payload: "p"}).Marshal()
	// Unknown fields of every wire type are skipped.
	extra := append([]byte{}, b...)
	extra = append(extra, 9<<3|0, 0x96, 0x01)              // varint 150
	extra = append(extra, 10<<3|1, 1, 2, 3, 4, 5, 6, 7, 8) // fixed64
	extra = append(extra, 11<<3|5, 1, 2, 3, 4)             // fixed32
	extra = append(extra, 12<<3|2, 2, 'h', 'i')            // bytes
	if m, err := Unmarshal(extra); err != nil || m.Payload != "p" {
		t.Fatalf("unknown fields: %+v %v", m, err)
	}
	for i := 1; i < len(b); i++ {
		if b[i-1] == 6<<3|2 { // cut inside the payload length-delimited field
			if _, err := Unmarshal(b[:i+1]); err == nil {
				t.Errorf("truncated message accepted")
			}
		}
	}
	if _, err := Unmarshal([]byte{0x0b}); err == nil { // wire type 3 (groups)
		t.Error("group wire type accepted")
	}
	huge := []byte{0x7f, 0xff, 0xff, 0xff}
	if _, err := ReadMessage(bytes.NewReader(huge)); err == nil {
		t.Error("oversized frame accepted")
	}
}

func TestParseSSDPResponse(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: http://192.168.1.20:49152/description.xml\r\nSERVER: Linux/4.9 UPnP/1.0 TV/1.0\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\nUSN: uuid:1234-abcd::urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"
	r, err := ParseSSDPResponse([]byte(raw))
	if err != nil || r.Location != "http://192.168.1.20:49152/description.xml" || r.ST != rendererType || !strings.HasPrefix(r.USN, "uuid:1234-abcd") {
		t.Fatalf("got %+v %v", r, err)
	}
	if _, err := ParseSSDPResponse([]byte("NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\n\r\n")); err == nil {
		t.Error("a NOTIFY was taken as an answer")
	}
	if _, err := ParseSSDPResponse([]byte("HTTP/1.1 200 OK\r\nST: x\r\n\r\n")); err == nil {
		t.Error("answer without location accepted")
	}
	from := netip.MustParseAddr("192.168.1.20")
	if _, err := checkLocation(r.Location, from); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"http://192.168.1.21:80/d.xml", "https://192.168.1.20/d.xml", "http://user@192.168.1.20/d.xml", "http://tv.local/d.xml", "file:///etc/passwd"} {
		if _, err := checkLocation(bad, from); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	q := string(mSearch(rendererType, 2))
	if !strings.HasPrefix(q, "M-SEARCH * HTTP/1.1\r\n") || !strings.Contains(q, "MAN: \"ssdp:discover\"\r\n") || !strings.Contains(q, "ST: "+rendererType+"\r\n") || !strings.HasSuffix(q, "\r\n\r\n") {
		t.Errorf("bad M-SEARCH: %q", q)
	}
}

const description = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
 <specVersion><major>1</major><minor>0</minor></specVersion>
 <device>
  <deviceType>urn:schemas-upnp-org:device:Basic:1</deviceType>
  <friendlyName>Box</friendlyName>
  <deviceList>
   <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>Living room TV</friendlyName>
    <manufacturer>ACME</manufacturer>
    <modelName>X1</modelName>
    <UDN>uuid:tv-1</UDN>
    <serviceList>
     <service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType><controlURL>/rc/control</controlURL></service>
     <service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType><controlURL>av/control</controlURL></service>
     <service><serviceType>urn:schemas-upnp-org:service:ConnectionManager:1</serviceType><controlURL>http://203.0.113.9/cm</controlURL></service>
    </serviceList>
   </device>
  </deviceList>
 </device>
</root>`

func TestParseDescription(t *testing.T) {
	loc, _ := url.Parse("http://192.168.1.20:49152/dev/description.xml")
	r, err := ParseDescription([]byte(description), loc)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "dlna:uuid:tv-1" || r.Name != "Living room TV" || r.Model != "ACME X1" || r.Kind != "dlna" || r.Addr != "192.168.1.20" {
		t.Errorf("got %+v", r)
	}
	if r.AVTransport != "http://192.168.1.20:49152/dev/av/control" || r.RenderingControl != "http://192.168.1.20:49152/rc/control" {
		t.Errorf("control URLs %s %s", r.AVTransport, r.RenderingControl)
	}
	// A control URL on another host is ignored: no usable AVTransport.
	evil := strings.Replace(description, "av/control", "http://203.0.113.9/av", 1)
	if _, err := ParseDescription([]byte(evil), loc); err == nil {
		t.Error("foreign control URL accepted")
	}
	server := strings.Replace(description, "AVTransport", "ContentDirectory", 1)
	if _, err := ParseDescription([]byte(server), loc); err == nil {
		t.Error("device without AVTransport accepted")
	}
}

func TestSOAP(t *testing.T) {
	env := SOAPEnvelope(avTransportType, "SetAVTransportURI", []Arg{{"InstanceID", "0"}, {"CurrentURI", "http://nas/a?x=1&y=2"}, {"CurrentURIMetaData", `<DIDL-Lite a="b"/>`}})
	var v struct {
		Body struct {
			Action struct {
				XMLName  xml.Name
				Instance string `xml:"InstanceID"`
				URI      string `xml:"CurrentURI"`
				Meta     string `xml:"CurrentURIMetaData"`
			} `xml:",any"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal([]byte(env), &v); err != nil {
		t.Fatal(err)
	}
	a := v.Body.Action
	if a.XMLName.Local != "SetAVTransportURI" || a.XMLName.Space != avTransportType || a.Instance != "0" || a.URI != "http://nas/a?x=1&y=2" || a.Meta != `<DIDL-Lite a="b"/>` {
		t.Errorf("envelope decoded as %+v", a)
	}
	if !strings.Contains(env, "&amp;y=2") || !strings.Contains(env, "&lt;DIDL-Lite") {
		t.Error("arguments not escaped")
	}

	resp := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetPositionInfoResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><Track>1</Track><TrackDuration>0:01:30</TrackDuration><RelTime>0:00:12.500</RelTime></u:GetPositionInfoResponse></s:Body></s:Envelope>`
	out, err := ParseSOAPResponse([]byte(resp))
	if err != nil || out["TrackDuration"] != "0:01:30" || out["RelTime"] != "0:00:12.500" || ParseTime(out["RelTime"]) != 12.5 || ParseTime(out["TrackDuration"]) != 90 {
		t.Errorf("got %v %v", out, err)
	}
	fault := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>714</errorCode><errorDescription>Illegal MIME-type</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`
	_, err = ParseSOAPResponse([]byte(fault))
	se, ok := err.(*SOAPError)
	if !ok || se.Code != "714" || se.Description != "Illegal MIME-type" {
		t.Errorf("fault: %v", err)
	}
	if _, err := ParseSOAPResponse([]byte("<a><b>")); err == nil {
		t.Error("broken XML accepted")
	}
	if FormatTime(3725.9) != "1:02:05" || FormatTime(-3) != "0:00:00" || ParseTime("NOT_IMPLEMENTED") != 0 {
		t.Error("time formatting")
	}
}

func TestDIDL(t *testing.T) {
	d := DIDL(`Film "A" & B`, "http://10.0.0.2:7070/api/media/stream/t/a&b.mp4", "video/mp4", "video", 1234)
	var v struct {
		Item struct {
			Title string `xml:"http://purl.org/dc/elements/1.1/ title"`
			Class string `xml:"urn:schemas-upnp-org:metadata-1-0/upnp/ class"`
			Res   struct {
				Protocol string `xml:"protocolInfo,attr"`
				Size     string `xml:"size,attr"`
				URL      string `xml:",chardata"`
			} `xml:"res"`
		} `xml:"item"`
	}
	if err := xml.Unmarshal([]byte(d), &v); err != nil {
		t.Fatal(err)
	}
	if v.Item.Title != `Film "A" & B` || v.Item.Class != "object.item.videoItem" || v.Item.Res.URL != "http://10.0.0.2:7070/api/media/stream/t/a&b.mp4" || v.Item.Res.Size != "1234" {
		t.Errorf("got %+v", v.Item)
	}
	if v.Item.Res.Protocol != "http-get:*:video/mp4:DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000" {
		t.Errorf("protocolInfo %s", v.Item.Res.Protocol)
	}
	if !strings.Contains(DIDL("p", "u", "image/jpeg", "image", 0), "object.item.imageItem.photo") {
		t.Error("picture class")
	}
}

func TestDLNARenderer(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	var uri string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a := strings.Trim(r.Header.Get("SOAPAction"), `"`)
		mu.Lock()
		actions = append(actions, a[strings.Index(a, "#")+1:])
		if strings.Contains(string(b), "SetAVTransportURI") {
			uri = string(b)
		}
		mu.Unlock()
		name := a[strings.Index(a, "#")+1:]
		body := ""
		switch name {
		case "GetTransportInfo":
			body = "<CurrentTransportState>PLAYING</CurrentTransportState>"
		case "GetPositionInfo":
			body = "<RelTime>0:00:07</RelTime><TrackDuration>0:10:00</TrackDuration>"
		case "GetVolume":
			body = "<CurrentVolume>35</CurrentVolume>"
		}
		w.Write([]byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:` + name + `Response xmlns:u="x">` + body + `</u:` + name + `Response></s:Body></s:Envelope>`))
	}))
	defer srv.Close()
	r := Receiver{Kind: "dlna", AVTransport: srv.URL + "/av", RenderingControl: srv.URL + "/rc"}
	p, _ := Open(context.Background(), r)
	ctx := context.Background()
	if err := p.Load(ctx, Media{URL: "http://nas/x.mp4", Type: "video/mp4", Kind: "video", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetVolume(ctx, 150); err != nil {
		t.Fatal(err)
	}
	st := p.Status(ctx)
	if st.State != "PLAYING" || st.Position != 7 || st.Duration != 600 || st.Volume != 35 {
		t.Errorf("status %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(actions[:4], ","); got != "Stop,SetAVTransportURI,Play,SetVolume" {
		t.Errorf("actions %s", got)
	}
	if !strings.Contains(uri, "<CurrentURI>http://nas/x.mp4</CurrentURI>") || !strings.Contains(uri, "&lt;DIDL-Lite") {
		t.Errorf("SetAVTransportURI body %s", uri)
	}
}

func TestParseCastAnswer(t *testing.T) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	_ = b.StartAnswers()
	inst := dnsmessage.MustNewName("Chromecast-abc._googlecast._tcp.local.")
	host := dnsmessage.MustNewName("abc.local.")
	hdr := func(n dnsmessage.Name, typ dnsmessage.Type) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: n, Type: typ, Class: dnsmessage.ClassINET, TTL: 120}
	}
	_ = b.PTRResource(hdr(dnsmessage.MustNewName(castService), dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: inst})
	_ = b.StartAdditionals()
	_ = b.SRVResource(hdr(inst, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 8009, Target: host})
	_ = b.TXTResource(hdr(inst, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{"id=abc123", "md=Chromecast", "fn=Salon", "ca=4101"}})
	_ = b.AResource(hdr(host, dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 30}})
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	insts, hosts := map[string]*castInstance{}, map[string]netip.Addr{}
	parseCastAnswer(msg, netip.MustParseAddr("192.168.1.99"), insts, hosts)
	rs := castReceivers(insts, hosts)
	if len(rs) != 1 || rs[0].ID != "cc:abc123" || rs[0].Name != "Salon" || rs[0].Addr != "192.168.1.30:8009" || rs[0].Model != "Chromecast" || !rs[0].Video {
		t.Fatalf("got %+v", rs)
	}
	// A speaker (no video capability bit).
	insts["chromecast-abc._googlecast._tcp.local."].audioOnly = true
	if rs := castReceivers(insts, hosts); rs[0].Video {
		t.Error("audio-only speaker offered for video")
	}
	parseCastAnswer([]byte{1, 2, 3}, netip.Addr{}, insts, hosts) // garbage is ignored
	if q, err := mdnsQuery(castService); err != nil || len(q) < 12 {
		t.Errorf("query %v", err)
	}
}

func TestMediaType(t *testing.T) {
	for name, want := range map[string]string{"a.MP4": "video/mp4 video", "b.flac": "audio/flac audio", "c.jpeg": "image/jpeg image", "d.html": " ", "e": " "} {
		m, k := MediaType(name)
		if m+" "+k != want {
			t.Errorf("%s: %s %s", name, m, k)
		}
	}
}
