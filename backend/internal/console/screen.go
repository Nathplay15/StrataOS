package console

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"strataos/internal/qr"
)

// Style of a piece of text; the terminal maps it to colours, tests ignore it.
type Style int

const (
	Plain Style = iota
	Bold
	Dim
	Accent
	Good
	Warn
	Bad
	Selected
	QR // dark modules on a light background
)

type Seg struct {
	Text  string
	Style Style
}

type Line []Seg

func (l Line) width() int {
	n := 0
	for _, s := range l {
		n += utf8.RuneCountInString(s.Text)
	}
	return n
}

// Frame is one screen, line by line.
type Frame []Line

// Text is the frame without styles (strata console --render-once, tests).
func (f Frame) Text() string {
	var b strings.Builder
	for _, l := range f {
		var t strings.Builder
		for _, s := range l {
			t.WriteString(s.Text)
		}
		b.WriteString(strings.TrimRight(t.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

type page int

const (
	pageMain page = iota
	pageNetwork
	pageResetConfirm
	pageResetPending
	pageRecoveryConfirm
	pageRecovery
	pagePower
	pagePowerConfirm
)

// Action is what a key asks the program to do outside the model.
type Action int

const (
	ActNone Action = iota
	ActNetwork
	ActReset
	ActKeep
	ActUndo
	ActRecovery
	ActReboot
	ActPoweroff
	ActLogin
)

// Key is a key press: a character, or one of the special keys below.
type Key rune

const (
	KeyUp    Key = -1
	KeyDown  Key = -2
	KeyEnter Key = '\r'
	KeyEsc   Key = 27
)

// Model is the state of the screen. Render draws it; Key changes it.
type Model struct {
	Now           time.Time
	Width, Height int
	Hostname      string
	Addrs         []string
	// Status is nil while the daemon does not answer.
	Status  *Status
	Net     *Network
	Code    *Code
	Pending *Pending
	Msg     string
	Page    page
	Sel     int
	power   Action
}

type item struct {
	label string
	off   func(m *Model) string // why the item is unavailable ("" = available)
}

var menu = []item{
	{"Network details", nil},
	{"Reset the network to DHCP", func(m *Model) string {
		if m.Status == nil {
			return "service down"
		}
		return ""
	}},
	{"Administrator recovery code", func(m *Model) string {
		switch {
		case m.Status == nil:
			return "service down"
		case !m.Status.SetupDone:
			return "after setup"
		case !m.Status.Recovery:
			return "off in Settings"
		}
		return ""
	}},
	{"Restart or shut down", func(m *Model) string {
		if m.Status == nil {
			return "service down"
		}
		return ""
	}},
	{"Login prompt (administrators)", nil},
}

func (m *Model) unavailable(i int) string {
	if menu[i].off == nil {
		return ""
	}
	return menu[i].off(m)
}

func (m *Model) ports() (string, string) {
	h, s := "7070", "7443"
	if m.Status != nil {
		if m.Status.HTTPPort != "" {
			h = m.Status.HTTPPort
		}
		if m.Status.HTTPSPort != "" {
			s = m.Status.HTTPSPort
		}
	}
	return h, s
}

// MainURL is the address of the QR code: plain HTTP, which a phone opens
// without a certificate warning.
func (m *Model) MainURL() string {
	if len(m.Addrs) == 0 {
		return ""
	}
	h, _ := m.ports()
	return "http://" + m.Addrs[0] + ":" + h
}

// Key handles a key press and returns the action to perform.
func (m *Model) Key(k Key) Action {
	if k >= 'a' && k <= 'z' {
		k -= 'a' - 'A'
	}
	back := k == KeyEsc || k == 'Q'
	switch m.Page {
	case pageMain:
		m.Msg = ""
		switch {
		case k == KeyUp:
			m.Sel = (m.Sel + len(menu) - 1) % len(menu)
		case k == KeyDown:
			m.Sel = (m.Sel + 1) % len(menu)
		case k >= '1' && int(k-'1') < len(menu):
			m.Sel = int(k - '1')
			return m.open()
		case k == KeyEnter || k == ' ':
			return m.open()
		}
	case pageNetwork:
		if back || k == KeyEnter {
			m.Page = pageMain
		}
	case pageResetConfirm:
		if k == 'Y' {
			m.Page = pageMain
			return ActReset
		}
		if back || k == 'N' {
			m.Page = pageMain
		}
	case pageResetPending:
		switch {
		case k == 'K':
			return ActKeep
		case k == 'U':
			return ActUndo
		case back:
			m.Page = pageMain // the change keeps waiting; the main screen says so
		}
	case pageRecoveryConfirm:
		if k == 'Y' {
			m.Page = pageMain
			return ActRecovery
		}
		if back || k == 'N' {
			m.Page = pageMain
		}
	case pageRecovery:
		// Any key hides the code (it stays valid until used or expired).
		m.Code = nil
		m.Page = pageMain
		m.Msg = "Recovery code hidden: it stays valid until used or expired."
	case pagePower:
		switch {
		case k == 'R':
			m.power, m.Page = ActReboot, pagePowerConfirm
		case k == 'S':
			m.power, m.Page = ActPoweroff, pagePowerConfirm
		case back || k == 'N':
			m.Page = pageMain
		}
	case pagePowerConfirm:
		if k == 'Y' {
			m.Page = pageMain
			return m.power
		}
		if back || k == 'N' {
			m.Page = pageMain
		}
	}
	return ActNone
}

func (m *Model) open() Action {
	if why := m.unavailable(m.Sel); why != "" {
		m.Msg = menu[m.Sel].label + ": " + why + "."
		return ActNone
	}
	switch m.Sel {
	case 0:
		m.Page = pageNetwork
		return ActNetwork
	case 1:
		if m.Pending != nil {
			m.Page = pageResetPending
		} else {
			m.Page = pageResetConfirm
		}
	case 2:
		m.Page = pageRecoveryConfirm
	case 3:
		m.Page = pagePower
	case 4:
		return ActLogin
	}
	return ActNone
}

// Tick closes pages that time out.
func (m *Model) Tick(now time.Time) {
	m.Now = now
	if m.Code != nil && now.After(m.Code.Expires) {
		m.Code = nil
		if m.Page == pageRecovery {
			m.Page = pageMain
			m.Msg = "The recovery code expired."
		}
	}
}

// SetStatus takes a status from the daemon (nil: not answering).
func (m *Model) SetStatus(st *Status) {
	m.Status = st
	if st == nil {
		return
	}
	if st.NetworkDeadline != nil {
		m.Pending = &Pending{Deadline: *st.NetworkDeadline}
		return
	}
	if m.Pending != nil {
		m.Pending = nil
		if m.Page == pageResetPending {
			m.Page = pageMain
		}
		m.Msg = "The network change was not kept: the previous configuration is back."
	}
}

// ShowCode displays a code the daemon generated.
func (m *Model) ShowCode(c *Code) {
	m.Code = c
	m.Page = pageRecovery
}

// ShowPending displays a network reset waiting to be kept.
func (m *Model) ShowPending(p *Pending) {
	m.Pending = p
	m.Page = pageResetPending
}

// frame builds a frame line by line.
type frame struct{ lines []Line }

func (f *frame) add(segs ...Seg) { f.lines = append(f.lines, Line(segs)) }
func (f *frame) blank()          { f.lines = append(f.lines, nil) }
func (f *frame) text(style Style, format string, a ...any) {
	f.add(Seg{fmt.Sprintf(format, a...), style})
}

func seg(s string, st Style) Seg { return Seg{s, st} }

// The StrataOS mark: three layers.
var logo = []string{"▄▄▄▄▄▄▄▄  ", "▀▀▀▀▀▀    ", "▄▄▄▄▄▄▄▄▄▄"}

// Render draws the model.
func Render(m *Model) Frame {
	w, h := m.Width, m.Height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 25
	}
	var f frame
	host, version := m.Hostname, ""
	if m.Status != nil {
		version = m.Status.Version
		if m.Status.Hostname != "" {
			host = m.Status.Hostname
		}
	}
	f.blank()
	f.add(seg("  ", Plain), seg(logo[0], Accent), seg("  ", Plain), seg("Strata", Bold), seg("OS", Accent), seg(" "+version, Dim))
	f.add(seg("  ", Plain), seg(logo[1], Accent), seg("  ", Plain), seg(host, Bold))
	f.add(seg("  ", Plain), seg(logo[2], Accent))
	f.blank()

	switch m.Page {
	case pageNetwork:
		m.renderNetwork(&f)
	case pageResetConfirm:
		f.text(Bold, "  Reset the network configuration")
		f.blank()
		f.text(Plain, "  Every network interface goes back to automatic addressing (DHCP).")
		f.text(Plain, "  Static addresses, bonds, bridges and VLANs set up in StrataOS are")
		f.text(Plain, "  removed (virtual machines on a bridge lose their network).")
		f.blank()
		f.text(Plain, "  You then have 3 minutes to keep the new configuration from this")
		f.text(Plain, "  screen; otherwise the previous one comes back by itself.")
		f.blank()
		f.add(seg("  [Y] ", Bold), seg("Reset the network", Plain), seg("    [N] ", Bold), seg("Cancel", Plain))
	case pageResetPending:
		left := time.Duration(0)
		if m.Pending != nil {
			left = max(0, m.Pending.Deadline.Sub(m.Now))
		}
		f.text(Bold, "  Network reset to DHCP")
		f.blank()
		m.renderAddrs(&f)
		f.blank()
		f.text(Warn, "  Undone automatically in %s unless you keep it.", clock(left))
		f.blank()
		f.add(seg("  [K] ", Bold), seg("Keep this configuration", Plain), seg("    [U] ", Bold), seg("Undo now", Plain), seg("    [Esc] ", Bold), seg("Back", Plain))
	case pageRecoveryConfirm:
		f.text(Bold, "  Administrator recovery code")
		f.blank()
		f.text(Plain, "  A one-time code, valid 15 minutes, lets someone reset the password")
		f.text(Plain, "  of an administrator on the sign-in page of the web interface")
		f.text(Plain, "  (\"Forgot password?\"). Administrators are notified.")
		f.blank()
		f.add(seg("  [Y] ", Bold), seg("Generate a code", Plain), seg("    [N] ", Bold), seg("Cancel", Plain))
	case pageRecovery:
		if m.Code != nil {
			f.text(Bold, "  Administrator recovery code")
			f.blank()
			f.add(seg("      ", Plain), seg(" "+m.Code.Code+" ", Selected))
			f.blank()
			f.text(Plain, "  Valid until %s (%s left), for one use.", m.Code.Expires.Local().Format("15:04"), clock(max(0, m.Code.Expires.Sub(m.Now))))
			f.blank()
			if u := m.MainURL(); u != "" {
				f.text(Plain, "  Open %s, choose \"Forgot password?\" on the sign-in", u)
				f.text(Plain, "  page and enter this code.")
			} else {
				f.text(Plain, "  On the sign-in page, choose \"Forgot password?\" and enter this code.")
			}
			f.blank()
			f.text(Dim, "  Press any key to hide the code.")
		}
	case pagePower:
		f.text(Bold, "  Restart or shut down")
		f.blank()
		f.add(seg("  [R] ", Bold), seg("Restart", Plain), seg("    [S] ", Bold), seg("Shut down", Plain), seg("    [Esc] ", Bold), seg("Cancel", Plain))
	case pagePowerConfirm:
		what := "Restart"
		if m.power == ActPoweroff {
			what = "Shut down"
		}
		f.text(Bold, "  %s the NAS now?", what)
		f.blank()
		f.text(Plain, "  Users lose access to their files until it is back.")
		f.blank()
		f.add(seg("  [Y] ", Bold), seg(what, Plain), seg("    [N] ", Bold), seg("Cancel", Plain))
	default:
		return m.renderMain(f, w, h)
	}
	if m.Msg != "" {
		f.blank()
		f.text(Warn, "  %s", m.Msg)
	}
	return fit(f.lines, w, h)
}

func (m *Model) renderAddrs(f *frame) {
	hp, sp := m.ports()
	if len(m.Addrs) == 0 {
		f.text(Warn, "  No network address yet: check the cable (waiting for DHCP).")
		return
	}
	f.text(Plain, "  Web interface")
	for i, a := range m.Addrs {
		st := Plain
		if i == 0 {
			st = Accent
		}
		if i >= 3 {
			f.text(Dim, "    ... and %d more", len(m.Addrs)-i)
			break
		}
		f.add(seg("    ", Plain), seg("http://"+a+":"+hp, st))
		f.add(seg("    ", Plain), seg("https://"+a+":"+sp, Dim))
	}
}

func (m *Model) renderMain(f frame, w, h int) Frame {
	// The QR code goes on the right when there is room for it.
	var code []Line
	if u := m.MainURL(); u != "" {
		// Level L: any http://IPv4:port fits a version 2 symbol (29 columns
		// with its quiet zone), which leaves 50 columns of text on 80.
		if c, err := qr.Encode([]byte(u), qr.L); err == nil && w >= 44+c.Size+4+1 {
			code = HalfBlocks(c, 2)
			code = append(code, Line{seg(center("Scan to open", c.Size+4), Dim)})
		}
	}
	top := len(f.lines)
	header := f.lines
	f.lines = nil
	m.renderAddrs(&f)
	f.blank()
	switch {
	case m.Status == nil:
		f.text(Warn, "  The StrataOS service is not answering (starting?).")
	case !m.Status.SetupDone:
		f.text(Warn, "  Open this address to set up your NAS.")
	default:
		f.text(Dim, "  Manage the NAS from your browser at this address.")
	}
	if m.Pending != nil {
		f.text(Warn, "  A network reset waits to be kept (%s left): menu 2.", clock(max(0, m.Pending.Deadline.Sub(m.Now))))
	}
	if m.Status != nil {
		f.blank()
		if n := len(m.Status.Alerts); n == 0 {
			f.add(seg("  Health    ", Plain), seg("■ OK", Good))
		} else {
			label := "■ 1 alert"
			if n > 1 {
				label = fmt.Sprintf("■ %d alerts", n)
			}
			st := Warn
			for _, a := range m.Status.Alerts {
				if a.Level == "error" {
					st = Bad
				}
			}
			f.add(seg("  Health    ", Plain), seg(label, st))
			for i, a := range m.Status.Alerts {
				if i == 2 {
					break
				}
				f.text(Dim, "            %s", a.Message)
			}
		}
		for i, v := range m.Status.Volumes {
			label := "          "
			if i == 0 {
				label = "  Volumes "
			}
			if i == 4 {
				f.text(Dim, "%s  ... and %d more", label, len(m.Status.Volumes)-i)
				break
			}
			usage := "not mounted"
			if v.Total > 0 {
				usage = fmt.Sprintf("%d%% of %s", v.Used*100/v.Total, size(v.Total))
			}
			st := Plain
			if v.Health != "" && v.Health != "healthy" {
				st = Warn
				usage += " (" + v.Health + ")"
			}
			f.add(seg(label+"  ", Plain), seg(fmt.Sprintf("%-10s ", v.Name), st), seg(usage, st))
		}
	}
	body := f.lines
	f.lines = nil
	f.blank()
	for i, it := range menu {
		label := fmt.Sprintf(" %d  %s ", i+1, it.label)
		st := Plain
		if why := m.unavailable(i); why != "" {
			st = Dim
			label += "(" + why + ") "
		}
		if i == m.Sel {
			f.add(seg("  ", Plain), seg(label, Selected))
		} else {
			f.add(seg("  ", Plain), seg(label, st))
		}
	}
	if m.Msg != "" {
		f.text(Warn, "  %s", m.Msg)
	}
	f.text(Dim, "  Arrows or 1-5 to choose, Enter to open.  Login prompt: Alt+F2.")

	// On a small screen the body gives way, not the menu.
	if extra := len(header) + len(body) + len(f.lines) - h; extra > 0 {
		body = body[:max(0, len(body)-extra)]
	}
	lines := append(append(header, body...), f.lines...)
	if code != nil {
		col := w - len([]rune(code[0][0].Text)) - 1
		for i, l := range code {
			for top+i >= len(lines) {
				lines = append(lines, nil)
			}
			lines[top+i] = join(lines[top+i], col, l)
		}
	}
	return fit(lines, w, h)
}

// join puts right at column col of left (left is cut or padded to col).
func join(left Line, col int, right Line) Line {
	var out Line
	n := 0
	for _, s := range left {
		r := []rune(s.Text)
		if n+len(r) > col-1 {
			r = r[:max(0, col-1-n)]
		}
		out = append(out, Seg{string(r), s.Style})
		n += len(r)
		if n >= col-1 {
			break
		}
	}
	out = append(out, Seg{strings.Repeat(" ", col-n), Plain})
	return append(out, right...)
}

// fit cuts lines to the screen width and the frame to its height.
func fit(lines []Line, w, h int) Frame {
	if len(lines) > h {
		lines = lines[:h]
	}
	out := make(Frame, len(lines))
	for i, l := range lines {
		if l.width() <= w {
			out[i] = l
			continue
		}
		n := 0
		for _, s := range l {
			r := []rune(s.Text)
			if n+len(r) > w {
				r = r[:w-n]
			}
			out[i] = append(out[i], Seg{string(r), s.Style})
			n += len(r)
			if n >= w {
				break
			}
		}
	}
	return out
}

// HalfBlocks draws a QR code two modules per character cell, with a quiet
// zone of the given width.
func HalfBlocks(c *qr.Code, quiet int) []Line {
	n := c.Size + 2*quiet
	var out []Line
	for y := 0; y < n; y += 2 {
		var b strings.Builder
		for x := 0; x < n; x++ {
			top := c.Black(x-quiet, y-quiet)
			bottom := y+1 < n && c.Black(x-quiet, y+1-quiet)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteByte(' ')
			}
		}
		out = append(out, Line{{b.String(), QR}})
	}
	return out
}

func (m *Model) renderNetwork(f *frame) {
	f.text(Bold, "  Network details")
	f.blank()
	if m.Net == nil {
		f.text(Dim, "  (details unavailable: the StrataOS service is not answering)")
		for _, a := range m.Addrs {
			f.text(Plain, "  %s", a)
		}
	} else {
		for _, i := range m.Net.Interfaces {
			if i.Kind == "docker" {
				continue
			}
			speed := ""
			if i.Speed > 0 {
				speed = fmt.Sprintf("%d Mb/s", i.Speed)
			}
			st := Good
			if i.State != "UP" && i.State != "UNKNOWN" {
				st = Dim
			}
			f.add(seg(fmt.Sprintf("  %-14s ", i.Name), Bold), seg(fmt.Sprintf("%-8s ", strings.ToLower(i.State)), st), seg(fmt.Sprintf("%-18s %s", i.MAC, speed), Dim))
			for _, a := range i.Addresses {
				f.text(Plain, "                 %s", a)
			}
		}
		f.blank()
		gw := m.Net.Gateway
		if gw == "" {
			gw = "none"
		}
		f.text(Plain, "  Gateway        %s", gw)
		f.text(Plain, "  DNS            %s", strings.Join(m.Net.DNS, ", "))
	}
	f.blank()
	f.text(Dim, "  Press Enter or Esc to go back.")
}

func clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func center(s string, w int) string {
	pad := max(0, (w-utf8.RuneCountInString(s))/2)
	return strings.Repeat(" ", pad) + s
}

func size(b int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	f := float64(b)
	i := 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if f >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
