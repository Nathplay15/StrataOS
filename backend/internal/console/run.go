package console

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Options of strata console.
type Options struct {
	Socket string
	// RenderOnce prints one frame as plain text and returns (tests).
	RenderOnce bool
	Out        io.Writer
}

// VT_ACTIVATE switches the visible virtual console (linux/vt.h).
const vtActivate = 0x5606

var ansi = map[Style]string{
	Plain: "\x1b[0m", Bold: "\x1b[0;1m", Dim: "\x1b[0;2m", Accent: "\x1b[0;1;36m",
	Good: "\x1b[0;1;32m", Warn: "\x1b[0;1;33m", Bad: "\x1b[0;1;31m", Selected: "\x1b[0;7m",
	QR: "\x1b[0;30;47m",
}

// ANSI draws the frame from the top left corner of a terminal.
func (f Frame) ANSI() string {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range f {
		for _, s := range l {
			b.WriteString(ansi[s.Style])
			b.WriteString(s.Text)
		}
		b.WriteString("\x1b[0m\x1b[K")
		if i < len(f)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[0m\x1b[J")
	return b.String()
}

// parseKeys turns bytes read from the terminal into keys.
func parseKeys(b []byte) []Key {
	var out []Key
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 27 && i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O'):
			switch b[i+2] {
			case 'A':
				out = append(out, KeyUp)
			case 'B':
				out = append(out, KeyDown)
			}
			i += 2
			// Skip the rest of longer sequences (F keys: ESC [ 1 1 ~).
			for i+1 < len(b) && b[i] >= '0' && b[i] <= '9' {
				i++
			}
		case c == '\n' || c == '\r':
			out = append(out, KeyEnter)
		case c == 27 || c >= ' ' && c < 127:
			out = append(out, Key(c))
		}
	}
	return out
}

// Run shows the screen until the program is stopped.
func Run(o Options) error {
	if o.Socket == "" {
		o.Socket = Socket
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	cl := NewClient(o.Socket)
	m := &Model{Now: time.Now(), Width: 80, Height: 25}
	refresh := func() *Status {
		st, err := cl.Status()
		if err != nil {
			return nil
		}
		return st
	}
	local := func() {
		m.Hostname, _ = os.Hostname()
		m.Addrs = LANAddresses()
	}
	if o.RenderOnce {
		local()
		m.SetStatus(refresh())
		_, err := io.WriteString(o.Out, Render(m).Text())
		return err
	}

	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return fmt.Errorf("not a terminal: %w", err)
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	raw.Iflag &^= unix.IXON | unix.ICRNL
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return err
	}
	out := o.Out
	// UTF-8 mode of the Linux console, no cursor, cleared screen.
	io.WriteString(out, "\x1b%G\x1b[?25l\x1b[2J")
	restore := func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
		io.WriteString(out, "\x1b[0m\x1b[2J\x1b[H\x1b[?25h")
	}
	defer restore()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)

	keys := make(chan []byte)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			keys <- append([]byte{}, buf[:n]...)
		}
	}()
	// Daemon calls run in the background; their results come back as
	// functions applied to the model by this loop, which alone touches it.
	results := make(chan func(), 8)
	gen := 0 // bumped by each action: older status answers are dropped
	fetching := false
	fetch := func() {
		if fetching {
			return
		}
		fetching = true
		g := gen
		go func() {
			st := refresh()
			results <- func() {
				fetching = false
				if g == gen {
					local()
					m.SetStatus(st)
				}
			}
		}()
	}
	do := func(a Action) {
		if a == ActNone {
			return
		}
		gen++
		if a == ActLogin {
			// logind starts a login prompt on the console switched to.
			if err := unix.IoctlSetInt(fd, vtActivate, 2); err != nil {
				m.Msg = "Cannot switch console: " + err.Error()
			} else {
				m.Msg = "Login prompt on console 2 (Alt+F2). Alt+F1 brings this screen back."
			}
			return
		}
		go func() {
			var f func()
			switch a {
			case ActNetwork:
				n, err := cl.Network()
				f = func() {
					if err != nil {
						n = nil
					}
					m.Net = n
				}
			case ActReset:
				p, err := cl.ResetNetwork()
				f = func() {
					if err != nil {
						m.Msg = "Network reset failed: " + err.Error()
						return
					}
					m.ShowPending(p)
				}
			case ActKeep:
				err := cl.KeepNetwork()
				f = func() {
					if err != nil {
						m.Msg = err.Error()
						return
					}
					m.Pending, m.Page, m.Msg = nil, pageMain, "Network configuration kept."
				}
			case ActUndo:
				err := cl.UndoNetwork()
				f = func() {
					if err != nil {
						m.Msg = err.Error()
						return
					}
					m.Pending, m.Page, m.Msg = nil, pageMain, "Previous network configuration restored."
				}
			case ActRecovery:
				c, err := cl.RecoveryCode()
				f = func() {
					if err != nil {
						m.Msg = "No recovery code: " + err.Error()
						return
					}
					m.ShowCode(c)
				}
			case ActReboot, ActPoweroff:
				action := "reboot"
				if a == ActPoweroff {
					action = "poweroff"
				}
				err := cl.Power(action)
				f = func() {
					if err != nil {
						m.Msg = err.Error()
					} else if a == ActReboot {
						m.Msg = "Restarting..."
					} else {
						m.Msg = "Shutting down..."
					}
				}
			}
			results <- func() { gen++; f() }
		}()
	}

	local()
	m.SetStatus(refresh())
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last, lastKey, n := "", time.Now(), 0
	for {
		if ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil && ws.Col > 0 && ws.Row > 0 {
			m.Width, m.Height = int(ws.Col), int(ws.Row)
		}
		if s := Render(m).ANSI(); s != last {
			io.WriteString(out, s)
			last = s
		}
		select {
		case <-stop:
			return nil
		case b, ok := <-keys:
			if !ok {
				return nil // terminal hung up
			}
			lastKey = time.Now()
			for _, k := range parseKeys(b) {
				do(m.Key(k))
			}
		case f := <-results:
			f()
		case now := <-tick.C:
			m.Tick(now)
			// Back to the main screen when nobody is there (a pending network
			// reset and a shown code close by themselves).
			if now.Sub(lastKey) > 3*time.Minute && m.Page != pageMain && m.Page != pageRecovery && m.Page != pageResetPending {
				m.Page = pageMain
			}
			if n++; n%3 == 0 {
				fetch()
			}
			if n%60 == 0 {
				last = "" // redraw everything now and then (kernel messages)
				io.WriteString(out, "\x1b[2J")
			}
		}
	}
}
