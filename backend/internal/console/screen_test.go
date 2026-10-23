package console

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"strataos/internal/qr"
)

var now = time.Date(2026, 10, 22, 14, 0, 0, 0, time.Local)

func model(setup bool) *Model {
	return &Model{
		Now: now, Width: 80, Height: 25, Hostname: "fallback",
		Addrs: []string{"192.168.1.20", "10.0.0.5"},
		Status: &Status{
			Hostname: "nas", Version: "0.7.0", SetupDone: setup, HTTPPort: "7070", HTTPSPort: "7443", Recovery: true,
			Alerts:  []Alert{{Level: "warning", Message: "Volume main is 91% full"}},
			Volumes: []Volume{{Name: "main", Used: 910, Total: 1000, Health: "healthy"}, {Name: "fast", Used: 0, Total: 2_000_000_000_000, Health: "degraded"}},
		},
	}
}

func checkSize(t *testing.T, f Frame, w, h int) {
	t.Helper()
	if len(f) > h {
		t.Errorf("%d lines for a %d-line screen", len(f), h)
	}
	for i, l := range f {
		if l.width() > w {
			t.Errorf("line %d is %d columns wide: %q", i, l.width(), strings.TrimRight(f[i:i+1].Text(), "\n"))
		}
	}
}

func TestMainScreen(t *testing.T) {
	m := model(false)
	f := Render(m)
	text := f.Text()
	for _, want := range []string{"StrataOS 0.7.0", "nas", "http://192.168.1.20:7070", "https://192.168.1.20:7443", "http://10.0.0.5:7070", "https://10.0.0.5:7443",
		"Open this address to set up your NAS", "1 alert", "Volume main is 91% full", "main", "91% of 1.0 KB", "fast", "0% of 2.0 TB (degraded)",
		"1  Network details", "3  Administrator recovery code (after setup)", "5  Login prompt", "Scan to open"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	checkSize(t, f, 80, 25)

	m = model(true)
	text = Render(m).Text()
	if strings.Contains(text, "set up your NAS") || strings.Contains(text, "(after setup)") || !strings.Contains(text, "Manage the NAS from your browser") {
		t.Errorf("after setup:\n%s", text)
	}

	// Daemon down: addresses from the kernel, default ports, no health.
	m.Status = nil
	text = Render(m).Text()
	for _, want := range []string{"fallback", "http://192.168.1.20:7070", "not answering", "(service down)"} {
		if !strings.Contains(text, want) {
			t.Errorf("daemon down: missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "Health") {
		t.Error("no health without the daemon")
	}

	// No address yet: no QR code.
	m = model(true)
	m.Addrs = nil
	text = Render(m).Text()
	if !strings.Contains(text, "No network address yet") || strings.Contains(text, "Scan to open") {
		t.Errorf("no address:\n%s", text)
	}

	// Small screen: the menu stays, the QR code goes.
	m = model(true)
	m.Width, m.Height = 60, 18
	f = Render(m)
	text = f.Text()
	checkSize(t, f, 60, 18)
	if !strings.Contains(text, "5  Login prompt") || strings.Contains(text, "Scan to open") {
		t.Errorf("small screen:\n%s", text)
	}
}

// The QR code on the screen holds the main address, module for module.
func TestScreenQRCode(t *testing.T) {
	m := model(true)
	f := Render(m)
	var rows []string
	col := -1
	for _, l := range f {
		for i, s := range l {
			if s.Style == QR {
				if col < 0 {
					col = Line(l[:i]).width()
				}
				rows = append(rows, s.Text)
			}
		}
	}
	c, err := qr.Encode([]byte("http://192.168.1.20:7070"), qr.L)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 2 || len(rows) != (c.Size+4+1)/2 || col+c.Size+4 > 80 {
		t.Fatalf("version %d, %d rows at column %d", c.Version, len(rows), col)
	}
	for y := 0; y < c.Size+4; y++ {
		r := []rune(rows[y/2])
		if len(r) != c.Size+4 {
			t.Fatalf("row %d: %d columns", y/2, len(r))
		}
		for x := range r {
			var dark bool
			switch r[x] {
			case '█':
				dark = true
			case '▀':
				dark = y%2 == 0
			case '▄':
				dark = y%2 == 1
			}
			if dark != c.Black(x-2, y-2) {
				t.Fatalf("module %d,%d differs", x-2, y-2)
			}
		}
	}
}

func TestMenuKeys(t *testing.T) {
	m := model(false)
	if a := m.Key('3'); a != ActNone || !strings.Contains(m.Msg, "after setup") || m.Page != pageMain {
		t.Fatalf("recovery before setup: %v %q", a, m.Msg)
	}
	m = model(true)
	if a := m.Key('1'); a != ActNetwork || m.Page != pageNetwork {
		t.Fatal("network details")
	}
	m.Net = &Network{Interfaces: []Iface{{Name: "eth0", State: "UP", MAC: "52:54:00:12:34:56", Addresses: []string{"192.168.1.20/24"}, Speed: 1000, Kind: "physical"}}, Gateway: "192.168.1.1", DNS: []string{"192.168.1.1"}}
	text := Render(m).Text()
	for _, want := range []string{"eth0", "up", "52:54:00:12:34:56", "1000 Mb/s", "192.168.1.20/24", "Gateway        192.168.1.1"} {
		if !strings.Contains(text, want) {
			t.Errorf("network page: missing %q in\n%s", want, text)
		}
	}
	m.Key(KeyEsc)

	// Arrows and Enter: reset the network, confirmed.
	m.Key(KeyDown)
	if m.Key(KeyEnter) != ActNone || m.Page != pageResetConfirm {
		t.Fatal("reset asks first")
	}
	if m.Key('y') != ActReset {
		t.Fatal("reset confirmed")
	}
	m.ShowPending(&Pending{Deadline: now.Add(3 * time.Minute)})
	if text := Render(m).Text(); !strings.Contains(text, "Undone automatically in 3:00") {
		t.Errorf("pending page:\n%s", text)
	}
	if m.Key('k') != ActKeep {
		t.Fatal("keep")
	}
	// The daemon no longer reports it: kept elsewhere or rolled back.
	st := *m.Status
	m.SetStatus(&st)
	if m.Pending != nil || m.Page != pageMain || !strings.Contains(m.Msg, "not kept") {
		t.Fatal("pending change gone")
	}

	// Recovery code, shown until a key or its expiry.
	m.Sel = 0
	if m.Key('3') != ActNone || m.Page != pageRecoveryConfirm || m.Key('Y') != ActRecovery {
		t.Fatal("recovery code asks first")
	}
	m.ShowCode(&Code{Code: "ABCD-EFGH-JKMN", Expires: now.Add(15 * time.Minute)})
	text = Render(m).Text()
	for _, want := range []string{"ABCD-EFGH-JKMN", "Valid until " + now.Add(15*time.Minute).Format("15:04") + " (15:00 left)", "http://192.168.1.20:7070", "Forgot password?"} {
		if !strings.Contains(text, want) {
			t.Errorf("code page: missing %q in\n%s", want, text)
		}
	}
	m.Tick(now.Add(15*time.Minute + time.Second))
	if m.Page != pageMain || m.Code != nil || !strings.Contains(m.Msg, "expired") {
		t.Fatal("expired code still shown")
	}
	m.ShowCode(&Code{Code: "ABCD-EFGH-JKMN", Expires: now.Add(15 * time.Minute)})
	m.Key('x')
	if m.Code != nil || strings.Contains(Render(m).Text(), "ABCD") {
		t.Fatal("a key hides the code")
	}

	// Power: two steps.
	if m.Key('4') != ActNone || m.Key('s') != ActNone || m.Page != pagePowerConfirm || !strings.Contains(Render(m).Text(), "Shut down the NAS now?") {
		t.Fatal("power asks first")
	}
	if m.Key('n') != ActNone || m.Page != pageMain {
		t.Fatal("power cancelled")
	}
	m.Key('4')
	m.Key('r')
	if m.Key('y') != ActReboot {
		t.Fatal("restart")
	}
	if m.Key('5') != ActLogin {
		t.Fatal("login prompt")
	}
	m.Status.Recovery = false
	if m.Key('3') != ActNone || !strings.Contains(m.Msg, "off in Settings") {
		t.Fatal("recovery forbidden in settings")
	}
}

func TestEveryPageFits(t *testing.T) {
	m := model(true)
	m.Net = &Network{}
	m.Pending = &Pending{Deadline: now.Add(time.Minute)}
	m.Code = &Code{Code: "ABCD-EFGH-JKMN", Expires: now.Add(time.Minute)}
	m.Msg = "a message"
	for p := pageMain; p <= pagePowerConfirm; p++ {
		m.Page = p
		f := Render(m)
		checkSize(t, f, 80, 25)
		if !utf8.ValidString(f.Text()) {
			t.Errorf("page %d: invalid UTF-8", p)
		}
	}
}

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("1\x1b[A\x1b[B\r\x1b[11~y\x1b"))
	want := []Key{'1', KeyUp, KeyDown, KeyEnter, 'y', KeyEsc}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestAddresses(t *testing.T) {
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"enp2s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
		"enp1s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
	if d := defaultIface(route); d != "enp1s0" {
		t.Fatalf("default interface %q", d)
	}
	got := orderAddrs([]ifaceAddrs{
		{"docker0", []string{"172.17.0.1"}},
		{"enp2s0", []string{"10.0.0.5"}},
		{"br-1234", []string{"172.18.0.1"}},
		{"wg0", []string{"10.8.0.1"}},
		{"enp1s0", []string{"192.168.1.20", "192.168.1.21"}},
		{"br0", []string{"192.168.2.1"}},
	}, "enp1s0")
	if strings.Join(got, " ") != "192.168.1.20 192.168.1.21 10.0.0.5 192.168.2.1" {
		t.Fatalf("got %v", got)
	}
}
