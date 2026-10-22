package cast

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rendererType       = "urn:schemas-upnp-org:device:MediaRenderer:1"
	avTransportType    = "urn:schemas-upnp-org:service:AVTransport:1"
	renderingCtrlType  = "urn:schemas-upnp-org:service:RenderingControl:1"
	ssdpAddr           = "239.255.255.250:1900"
	maxDescriptionSize = 256 << 10
)

// SSDPPort is where M-SEARCH requests are sent (a variable for the tests).
var ssdpDest = ssdpAddr

func mSearch(st string, mx int) []byte {
	return []byte("M-SEARCH * HTTP/1.1\r\nHOST: " + ssdpAddr + "\r\nMAN: \"ssdp:discover\"\r\nMX: " + strconv.Itoa(mx) + "\r\nST: " + st + "\r\nUSER-AGENT: Linux UPnP/1.1 StrataOS/1\r\n\r\n")
}

// SSDPResponse is the useful part of an answer to an M-SEARCH.
type SSDPResponse struct {
	Location string
	ST       string
	USN      string
	Server   string
}

// ParseSSDPResponse reads an HTTP-over-UDP answer.
func ParseSSDPResponse(b []byte) (SSDPResponse, error) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(b)), nil)
	if err != nil {
		return SSDPResponse{}, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SSDPResponse{}, fmt.Errorf("ssdp: status %d", resp.StatusCode)
	}
	r := SSDPResponse{Location: resp.Header.Get("Location"), ST: resp.Header.Get("St"), USN: resp.Header.Get("Usn"), Server: resp.Header.Get("Server")}
	if r.Location == "" {
		return r, errors.New("ssdp: no location")
	}
	return r, nil
}

// checkLocation accepts the description URL of a device only on the address
// that answered, over plain HTTP: a forged answer cannot make the daemon
// fetch other hosts.
func checkLocation(loc string, from netip.Addr) (*url.URL, error) {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return nil, errors.New("ssdp: invalid location")
	}
	a, err := netip.ParseAddr(u.Hostname())
	if err != nil || a.Unmap() != from.Unmap() {
		return nil, errors.New("ssdp: location is not the answering device")
	}
	return u, nil
}

type xmlService struct {
	Type       string `xml:"serviceType"`
	ControlURL string `xml:"controlURL"`
}

type xmlDevice struct {
	DeviceType   string       `xml:"deviceType"`
	FriendlyName string       `xml:"friendlyName"`
	ModelName    string       `xml:"modelName"`
	Manufacturer string       `xml:"manufacturer"`
	UDN          string       `xml:"UDN"`
	Services     []xmlService `xml:"serviceList>service"`
	Devices      []xmlDevice  `xml:"deviceList>device"`
}

type xmlRoot struct {
	URLBase string    `xml:"URLBase"`
	Device  xmlDevice `xml:"device"`
}

// ParseDescription reads a device description and returns the renderer it
// holds (the root device or an embedded one).
func ParseDescription(b []byte, location *url.URL) (Receiver, error) {
	var root xmlRoot
	if err := xml.Unmarshal(b, &root); err != nil {
		return Receiver{}, fmt.Errorf("invalid device description: %w", err)
	}
	base := location
	if root.URLBase != "" {
		if u, err := url.Parse(root.URLBase); err == nil && u.Scheme == "http" && u.Hostname() == location.Hostname() {
			base = u
		}
	}
	var find func(d xmlDevice) *xmlDevice
	find = func(d xmlDevice) *xmlDevice {
		for _, s := range d.Services {
			if strings.HasPrefix(s.Type, "urn:schemas-upnp-org:service:AVTransport:") {
				return &d
			}
		}
		for _, c := range d.Devices {
			if f := find(c); f != nil {
				return f
			}
		}
		return nil
	}
	d := find(root.Device)
	if d == nil {
		return Receiver{}, errors.New("not a media renderer (no AVTransport service)")
	}
	r := Receiver{Kind: "dlna", Name: strings.TrimSpace(d.FriendlyName), Model: strings.TrimSpace(strings.TrimSpace(d.Manufacturer + " " + d.ModelName)), Addr: location.Hostname(), Video: true}
	r.ID = "dlna:" + strings.TrimSpace(d.UDN)
	if d.UDN == "" {
		r.ID = "dlna:" + location.Host
	}
	if r.Name == "" {
		r.Name = location.Hostname()
	}
	for _, s := range d.Services {
		ref, err := url.Parse(strings.TrimSpace(s.ControlURL))
		if err != nil {
			continue
		}
		u := base.ResolveReference(ref)
		if u.Hostname() != location.Hostname() || u.Scheme != "http" {
			continue // control URLs on other hosts are ignored
		}
		switch {
		case strings.HasPrefix(s.Type, "urn:schemas-upnp-org:service:AVTransport:"):
			r.AVTransport = u.String()
		case strings.HasPrefix(s.Type, "urn:schemas-upnp-org:service:RenderingControl:"):
			r.RenderingControl = u.String()
		}
	}
	if r.AVTransport == "" {
		return Receiver{}, errors.New("no usable AVTransport control URL")
	}
	return r, nil
}

var httpClient = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func fetchDescription(ctx context.Context, u *url.URL) (Receiver, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Receiver{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Receiver{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Receiver{}, fmt.Errorf("device description: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDescriptionSize))
	if err != nil {
		return Receiver{}, err
	}
	return ParseDescription(b, u)
}

// DiscoverRenderers sends an SSDP M-SEARCH for media renderers and reads
// the description of each one that answers.
func DiscoverRenderers(ctx context.Context, wait time.Duration) []Receiver {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil
	}
	defer conn.Close()
	dst, err := net.ResolveUDPAddr("udp4", ssdpDest)
	if err != nil {
		return nil
	}
	mx := max(1, int(wait/time.Second))
	q := mSearch(rendererType, mx)
	_, _ = conn.WriteToUDP(q, dst)
	go func() {
		time.Sleep(wait / 3)
		_, _ = conn.WriteToUDP(q, dst)
	}()
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	locs := map[string]netip.Addr{}
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			break
		}
		r, err := ParseSSDPResponse(buf[:n])
		if err != nil {
			continue
		}
		if len(locs) < 64 {
			locs[r.Location] = from.Addr().Unmap()
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := []Receiver{}
	for loc, from := range locs {
		u, err := checkLocation(loc, from)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := fetchDescription(ctx, u)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, r)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// ---- SOAP ----

// Arg is one argument of a UPnP action (the order matters to some devices).
type Arg struct{ Name, Value string }

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// SOAPEnvelope builds the body of a UPnP action call.
func SOAPEnvelope(service, action string, args []Arg) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	b.WriteString(`<u:` + action + ` xmlns:u="` + xmlEscape(service) + `">`)
	for _, a := range args {
		b.WriteString("<" + a.Name + ">" + xmlEscape(a.Value) + "</" + a.Name + ">")
	}
	b.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)
	return b.String()
}

// SOAPError is a UPnP fault returned by a device.
type SOAPError struct {
	Code        string
	Description string
}

func (e *SOAPError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("the receiver refused the command: %s (UPnP error %s)", e.Description, e.Code)
	}
	return fmt.Sprintf("the receiver refused the command (UPnP error %s)", e.Code)
}

// ParseSOAPResponse returns the output arguments of an action response, or
// the UPnP fault it carries.
func ParseSOAPResponse(b []byte) (map[string]string, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	out := map[string]string{}
	var stack []string
	var text strings.Builder
	fault := false
	var code, desc string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid SOAP response: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text.Reset()
			if t.Name.Local == "Fault" {
				fault = true
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			v := strings.TrimSpace(text.String())
			switch {
			case fault && t.Name.Local == "errorCode":
				code = v
			case fault && t.Name.Local == "errorDescription":
				desc = v
			case !fault && len(stack) == 4: // Envelope > Body > ActionResponse > Arg
				out[t.Name.Local] = v
			}
			stack = stack[:len(stack)-1]
			text.Reset()
		}
	}
	if fault {
		return nil, &SOAPError{Code: code, Description: desc}
	}
	return out, nil
}

// Call sends a SOAP action to a control URL.
func Call(ctx context.Context, controlURL, service, action string, args []Arg) (map[string]string, error) {
	body := SOAPEnvelope(service, action, args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+service+"#"+action+`"`)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out, perr := ParseSOAPResponse(b)
	if perr != nil {
		return nil, perr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", action, resp.StatusCode)
	}
	return out, nil
}

// ---- DIDL-Lite ----

// DIDL returns the DIDL-Lite metadata describing a stream URL.
func DIDL(title, url, mimeType, kind string, size int64) string {
	class := map[string]string{"video": "object.item.videoItem", "audio": "object.item.audioItem.musicTrack", "image": "object.item.imageItem.photo"}[kind]
	if class == "" {
		class = "object.item"
	}
	sizeAttr := ""
	if size > 0 {
		sizeAttr = ` size="` + strconv.FormatInt(size, 10) + `"`
	}
	return `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" xmlns:dlna="urn:schemas-dlna-org:metadata-1-0/">` +
		`<item id="strata-0" parentID="-1" restricted="1"><dc:title>` + xmlEscape(title) + `</dc:title><upnp:class>` + class + `</upnp:class>` +
		`<res protocolInfo="http-get:*:` + xmlEscape(mimeType) + `:` + ContentFeatures(kind) + `"` + sizeAttr + `>` + xmlEscape(url) + `</res></item></DIDL-Lite>`
}

// ContentFeatures is the DLNA.ORG fourth field of protocolInfo, also sent as
// the contentFeatures.dlna.org header: byte seeking for audio and video,
// streaming or interactive transfer.
func ContentFeatures(kind string) string {
	if kind == "image" {
		return "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=00f00000000000000000000000000000"
	}
	return "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"
}

// ---- Renderer control ----

func instance(extra ...Arg) []Arg { return append([]Arg{{"InstanceID", "0"}}, extra...) }

// FormatTime writes seconds as H:MM:SS for Seek.
func FormatTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

// ParseTime reads H:MM:SS(.fff) durations; "NOT_IMPLEMENTED" and garbage give 0.
func ParseTime(v string) float64 {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 3 {
		return 0
	}
	var total float64
	for _, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || f < 0 {
			return 0
		}
		total = total*60 + f
	}
	return total
}

func (r *Receiver) av(ctx context.Context, action string, args []Arg) (map[string]string, error) {
	return Call(ctx, r.AVTransport, avTransportType, action, args)
}

// DLNALoad sets the URL on the renderer and starts playback.
func (r *Receiver) DLNALoad(ctx context.Context, url, mimeType, title, kind string, size int64) error {
	_, _ = r.av(ctx, "Stop", instance()) // some renderers refuse a new URI while playing
	if _, err := r.av(ctx, "SetAVTransportURI", instance(Arg{"CurrentURI", url}, Arg{"CurrentURIMetaData", DIDL(title, url, mimeType, kind, size)})); err != nil {
		return err
	}
	return r.DLNAPlay(ctx)
}

func (r *Receiver) DLNAPlay(ctx context.Context) error {
	_, err := r.av(ctx, "Play", instance(Arg{"Speed", "1"}))
	return err
}

func (r *Receiver) DLNAPause(ctx context.Context) error {
	_, err := r.av(ctx, "Pause", instance())
	return err
}

func (r *Receiver) DLNAStop(ctx context.Context) error {
	_, err := r.av(ctx, "Stop", instance())
	return err
}

func (r *Receiver) DLNASeek(ctx context.Context, sec float64) error {
	_, err := r.av(ctx, "Seek", instance(Arg{"Unit", "REL_TIME"}, Arg{"Target", FormatTime(sec)}))
	return err
}

// DLNASetVolume takes 0-100.
func (r *Receiver) DLNASetVolume(ctx context.Context, v int) error {
	if r.RenderingControl == "" {
		return errors.New("this receiver has no volume control")
	}
	_, err := Call(ctx, r.RenderingControl, renderingCtrlType, "SetVolume", instance(Arg{"Channel", "Master"}, Arg{"DesiredVolume", strconv.Itoa(max(0, min(100, v)))}))
	return err
}

// DLNAStatus reads the transport state, position and volume.
func (r *Receiver) DLNAStatus(ctx context.Context) Status {
	st := Status{State: "UNKNOWN", Volume: -1}
	if t, err := r.av(ctx, "GetTransportInfo", instance()); err == nil {
		st.State = map[string]string{"PAUSED_PLAYBACK": "PAUSED", "NO_MEDIA_PRESENT": "IDLE", "TRANSITIONING": "BUFFERING"}[t["CurrentTransportState"]]
		if st.State == "" {
			st.State = t["CurrentTransportState"]
		}
	}
	if p, err := r.av(ctx, "GetPositionInfo", instance()); err == nil {
		st.Position, st.Duration = ParseTime(p["RelTime"]), ParseTime(p["TrackDuration"])
		st.Media = p["TrackURI"]
	}
	if r.RenderingControl != "" {
		if v, err := Call(ctx, r.RenderingControl, renderingCtrlType, "GetVolume", instance(Arg{"Channel", "Master"})); err == nil {
			if n, err := strconv.Atoi(v["CurrentVolume"]); err == nil {
				st.Volume = n
			}
		}
	}
	return st
}
