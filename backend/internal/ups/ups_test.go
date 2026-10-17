package ups

import (
	"strings"
	"testing"
)

func TestValidateAndRender(t *testing.T) {
	c := Config{Mode: "usb", Share: true, ShareUser: "monuser", SharePassword: "secret", ShutdownAfter: 300}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	f := Render(c)
	for file, want := range map[string]string{
		"nut.conf":      "MODE=netserver",
		"ups.conf":      "driver = usbhid-ups",
		"upsd.conf":     "LISTEN 0.0.0.0 3493",
		"upsd.users":    "[monuser]",
		"upsmon.conf":   "MONITOR ups@localhost 1 strata " + c.LocalPassword + " primary",
		"upssched.conf": "AT ONBATT * START-TIMER strata-shutdown 300",
	} {
		if !strings.Contains(f[file], want) {
			t.Errorf("%s: missing %q in:\n%s", file, want, f[file])
		}
	}

	n := Config{Mode: "netclient", RemoteHost: "192.168.1.5", RemoteUser: "monuser", RemotePassword: "secret"}
	if err := Validate(&n); err != nil {
		t.Fatal(err)
	}
	f = Render(n)
	if !strings.Contains(f["upsmon.conf"], "MONITOR ups@192.168.1.5:3493 1 monuser secret secondary") || !strings.Contains(f["nut.conf"], "MODE=netclient") {
		t.Errorf("netclient rendering:\n%s", f["upsmon.conf"])
	}
	if strings.Contains(f["upssched.conf"], "START-TIMER") {
		t.Error("no timer expected without a shutdown delay")
	}

	for name, bad := range map[string]Config{
		"newline in password": {Mode: "netclient", RemoteHost: "h", RemoteUser: "u", RemotePassword: "a\nMONITOR evil"},
		"quote in desc":       {Mode: "usb", Desc: `x" driver=evil`},
		"unknown driver":      {Mode: "usb", Driver: "../../bin/sh"},
		"bad host":            {Mode: "netclient", RemoteHost: "-oops", RemoteUser: "u", RemotePassword: "p"},
		"short share pass":    {Mode: "usb", Share: true, ShareUser: "m", SharePassword: "1"},
	} {
		if Validate(&bad) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := Config{Mode: "simulation"}
	_ = Validate(&s)
	if s.Driver != "dummy-ups" || s.Port != SimDevPath {
		t.Errorf("simulation defaults: %+v", s)
	}
}
