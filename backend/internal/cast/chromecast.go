package cast

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	nsConnection = "urn:x-cast:com.google.cast.tp.connection"
	nsHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"
	nsReceiver   = "urn:x-cast:com.google.cast.receiver"
	nsMedia      = "urn:x-cast:com.google.cast.media"
	// DefaultMediaReceiver plays a URL (video, audio or picture) without a custom app.
	DefaultMediaReceiver = "CC1AD845"
	senderID             = "sender-strata"
	receiverID           = "receiver-0"
)

// Chromecast is an open Cast v2 channel to one device.
type Chromecast struct {
	conn    net.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int
	waiting map[int]chan map[string]any
	done    chan struct{}
	err     error

	transport string  // transportId of the running media app
	session   int     // mediaSessionId of the loaded media
	position  float64 // last reported currentTime (seconds)
	posAt     time.Time
	duration  float64
	state     string // PLAYING, PAUSED, BUFFERING, IDLE
	volume    float64
}

// DialChromecast opens the TLS channel (devices use self-signed certificates
// that cannot be checked against anything) and starts the heartbeat.
func DialChromecast(ctx context.Context, addr string) (*Chromecast, error) {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Chromecast{conn: conn, waiting: map[int]chan map[string]any{}, done: make(chan struct{}), volume: -1}
	if err := c.send(receiverID, nsConnection, map[string]any{"type": "CONNECT"}); err != nil {
		conn.Close()
		return nil, err
	}
	go c.readLoop()
	go c.heartbeat()
	return c, nil
}

// Close ends the channel.
func (c *Chromecast) Close() error {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t != "" {
		_ = c.send(t, nsConnection, map[string]any{"type": "CLOSE"})
	}
	_ = c.send(receiverID, nsConnection, map[string]any{"type": "CLOSE"})
	return c.conn.Close()
}

// Done is closed when the channel is lost.
func (c *Chromecast) Done() <-chan struct{} { return c.done }

func (c *Chromecast) send(dest, ns string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return WriteMessage(c.conn, &Message{Source: senderID, Destination: dest, Namespace: ns, Payload: string(b)})
}

// request sends a payload with a requestId and waits for the answer carrying it.
func (c *Chromecast) request(ctx context.Context, dest, ns string, payload map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]any, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
	}()
	payload["requestId"] = id
	if err := c.send(dest, ns, payload); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case r := <-ch:
		switch r["type"] {
		case "LOAD_FAILED", "LOAD_CANCELLED", "INVALID_REQUEST", "LAUNCH_ERROR", "INVALID_PLAYER_STATE":
			reason, _ := r["reason"].(string)
			if reason == "" {
				reason, _ = r["detailedErrorCode"].(string)
			}
			return r, fmt.Errorf("receiver refused the request: %v %s", r["type"], reason)
		}
		return r, nil
	case <-c.done:
		return nil, c.lostErr()
	case <-ctx.Done():
		return nil, errors.New("the receiver did not answer")
	}
}

func (c *Chromecast) lostErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return fmt.Errorf("connection to the receiver lost: %w", c.err)
	}
	return errors.New("connection to the receiver lost")
}

func (c *Chromecast) heartbeat() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if c.send(receiverID, nsHeartbeat, map[string]any{"type": "PING"}) != nil {
				c.conn.Close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Chromecast) readLoop() {
	defer close(c.done)
	for {
		// A PING comes every 5 s at most: silence means the device is gone.
		_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		m, err := ReadMessage(c.conn)
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
			c.conn.Close()
			return
		}
		var p map[string]any
		if json.Unmarshal([]byte(m.Payload), &p) != nil {
			continue
		}
		switch m.Namespace {
		case nsHeartbeat:
			if p["type"] == "PING" {
				_ = c.send(m.Source, nsHeartbeat, map[string]any{"type": "PONG"})
			}
			continue
		case nsConnection:
			if p["type"] == "CLOSE" && m.Source != receiverID {
				c.mu.Lock()
				c.transport, c.session = "", 0
				c.mu.Unlock()
			}
			continue
		case nsReceiver:
			c.receiverStatus(p)
		case nsMedia:
			c.mediaStatus(p)
		}
		if id, ok := p["requestId"].(float64); ok && id > 0 {
			c.mu.Lock()
			ch := c.waiting[int(id)]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- p:
				default:
				}
			}
		}
	}
}

func (c *Chromecast) receiverStatus(p map[string]any) {
	st, _ := p["status"].(map[string]any)
	if st == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := st["volume"].(map[string]any); ok {
		if l, ok := v["level"].(float64); ok {
			c.volume = l
		}
	}
	apps, _ := st["applications"].([]any)
	found := false
	for _, a := range apps {
		m, _ := a.(map[string]any)
		if m["appId"] == DefaultMediaReceiver {
			if t, _ := m["transportId"].(string); t != "" {
				c.transport, found = t, true
			}
		}
	}
	if !found {
		c.transport, c.session = "", 0
	}
}

func (c *Chromecast) mediaStatus(p map[string]any) {
	list, _ := p["status"].([]any)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range list {
		m, _ := x.(map[string]any)
		if id, ok := m["mediaSessionId"].(float64); ok {
			c.session = int(id)
		}
		if t, ok := m["currentTime"].(float64); ok {
			c.position, c.posAt = t, time.Now()
		}
		if s, _ := m["playerState"].(string); s != "" {
			c.state = s
		}
		if md, ok := m["media"].(map[string]any); ok {
			if d, ok := md["duration"].(float64); ok {
				c.duration = d
			}
		}
	}
}

// Load starts the Default Media Receiver if needed and plays a URL.
func (c *Chromecast) Load(ctx context.Context, url, contentType, title string, image bool) error {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t == "" {
		if _, err := c.request(ctx, receiverID, nsReceiver, map[string]any{"type": "LAUNCH", "appId": DefaultMediaReceiver}); err != nil {
			return err
		}
		c.mu.Lock()
		t = c.transport
		c.mu.Unlock()
		if t == "" {
			return errors.New("the media receiver application did not start")
		}
		if err := c.send(t, nsConnection, map[string]any{"type": "CONNECT"}); err != nil {
			return err
		}
	}
	stream := "BUFFERED"
	meta := map[string]any{"metadataType": 0, "title": title}
	if image {
		stream = "NONE"
		meta = map[string]any{"metadataType": 4, "title": title}
	}
	_, err := c.request(ctx, t, nsMedia, map[string]any{
		"type": "LOAD", "autoplay": true, "currentTime": 0,
		"media": map[string]any{"contentId": url, "contentType": contentType, "streamType": stream, "metadata": meta},
	})
	return err
}

func (c *Chromecast) media(ctx context.Context, typ string, extra map[string]any) error {
	c.mu.Lock()
	t, s := c.transport, c.session
	c.mu.Unlock()
	if t == "" || s == 0 {
		return errors.New("nothing is playing on this receiver")
	}
	p := map[string]any{"type": typ, "mediaSessionId": s}
	for k, v := range extra {
		p[k] = v
	}
	_, err := c.request(ctx, t, nsMedia, p)
	return err
}

func (c *Chromecast) Play(ctx context.Context) error  { return c.media(ctx, "PLAY", nil) }
func (c *Chromecast) Pause(ctx context.Context) error { return c.media(ctx, "PAUSE", nil) }

// Stop closes the media app, which brings the device back to its idle screen.
func (c *Chromecast) Stop(ctx context.Context) error {
	_, err := c.request(ctx, receiverID, nsReceiver, map[string]any{"type": "STOP"})
	return err
}

func (c *Chromecast) Seek(ctx context.Context, seconds float64) error {
	return c.media(ctx, "SEEK", map[string]any{"currentTime": seconds})
}

// SetVolume takes a level between 0 and 1.
func (c *Chromecast) SetVolume(ctx context.Context, level float64) error {
	_, err := c.request(ctx, receiverID, nsReceiver, map[string]any{"type": "SET_VOLUME", "volume": map[string]any{"level": level}})
	return err
}

// Status asks the device for the media state and returns it.
func (c *Chromecast) Status(ctx context.Context) Status {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t != "" {
		_, _ = c.request(ctx, t, nsMedia, map[string]any{"type": "GET_STATUS"})
	}
	_, _ = c.request(ctx, receiverID, nsReceiver, map[string]any{"type": "GET_STATUS"})
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{State: c.state, Position: c.position, Duration: c.duration, Volume: -1}
	if c.volume >= 0 {
		st.Volume = int(c.volume*100 + 0.5)
	}
	if t == "" {
		st.State = "IDLE"
	}
	return st
}

// Status is the playback state of a receiver.
type Status struct {
	State    string  `json:"state"` // PLAYING, PAUSED, BUFFERING, STOPPED, IDLE…
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
	Volume   int     `json:"volume"` // 0-100, -1 when unknown
}
