package remote

import (
	"net/url"
	"testing"
)

func TestParseNATPMP(t *testing.T) {
	a, err := ParseNATPMP([]byte{0, 128, 0, 0, 0, 0, 1, 0, 100, 72, 3, 4})
	if err != nil || a.String() != "100.72.3.4" {
		t.Fatal(a, err)
	}
	if _, err := ParseNATPMP([]byte{0, 128, 0, 3, 0, 0, 1, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("error code")
	}
	if _, err := ParseNATPMP([]byte{0, 0, 0}); err == nil {
		t.Fatal("short")
	}
}

func TestSSDPLocation(t *testing.T) {
	r := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nLocation: http://192.168.1.1:49000/igddesc.xml\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n"
	if got := SSDPLocation([]byte(r)); got != "http://192.168.1.1:49000/igddesc.xml" {
		t.Fatal(got)
	}
	if SSDPLocation([]byte("NOTIFY * HTTP/1.1\r\nLOCATION: http://x/\r\n")) != "" {
		t.Fatal("not an answer")
	}
}

const igdDesc = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
 <device><deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
  <deviceList><device><deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
   <deviceList><device><deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
    <serviceList><service>
     <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
     <controlURL>/igdupnp/control/WANIPConn1</controlURL>
    </service></serviceList>
   </device></deviceList>
  </device></deviceList>
 </device>
</root>`

func TestParseIGD(t *testing.T) {
	base, _ := url.Parse("http://192.168.1.1:49000/igddesc.xml")
	u, typ, err := ParseIGD([]byte(igdDesc), base)
	if err != nil || u.String() != "http://192.168.1.1:49000/igdupnp/control/WANIPConn1" || typ != "urn:schemas-upnp-org:service:WANIPConnection:1" {
		t.Fatal(u, typ, err)
	}
	bad := `<root><device><serviceList><service><serviceType>urn:x"/><evil:WANIPConnection:1</serviceType><controlURL>/c</controlURL></service></serviceList></device></root>`
	if _, _, err := ParseIGD([]byte(bad), base); err == nil {
		t.Fatal("odd service type accepted")
	}
}

func TestParseExternalIP(t *testing.T) {
	b := `<s:Envelope><s:Body><u:GetExternalIPAddressResponse><NewExternalIPAddress>203.0.113.7</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`
	a, err := ParseExternalIP([]byte(b))
	if err != nil || a.String() != "203.0.113.7" {
		t.Fatal(a, err)
	}
	if _, err := ParseExternalIP([]byte("<x/>")); err == nil {
		t.Fatal("missing")
	}
}
