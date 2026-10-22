package cast

import (
	"context"
	"errors"
	"path"
	"strings"
)

// Media is what a receiver is asked to play.
type Media struct {
	URL   string
	Type  string // MIME type
	Kind  string // video | audio | image
	Title string
	Size  int64
}

// Player drives one receiver, whatever its protocol.
type Player interface {
	Load(ctx context.Context, m Media) error
	Play(ctx context.Context) error
	Pause(ctx context.Context) error
	Stop(ctx context.Context) error
	Seek(ctx context.Context, sec float64) error
	SetVolume(ctx context.Context, v int) error
	Status(ctx context.Context) Status
	Close() error
}

// Open connects to a receiver.
func Open(ctx context.Context, r Receiver) (Player, error) {
	switch r.Kind {
	case "chromecast":
		c, err := DialChromecast(ctx, r.Addr)
		if err != nil {
			return nil, err
		}
		return &ccPlayer{c}, nil
	case "dlna":
		rc := r
		return &dlnaPlayer{&rc}, nil
	}
	return nil, errors.New("unknown receiver type")
}

// Resume connects again to a receiver that was given a media before a
// restart of the daemon, without loading anything: the controls and Status
// act on what it plays.
func Resume(ctx context.Context, r Receiver) (Player, error) {
	if r.Kind != "chromecast" {
		return Open(ctx, r)
	}
	c, err := DialChromecast(ctx, r.Addr)
	if err != nil {
		return nil, err
	}
	if err := c.Attach(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return &ccPlayer{c}, nil
}

// Ended tells whether a receiver given a URL before a restart no longer
// plays it: it plays a URL without mark (a part unique to ours, receivers
// sometimes rewrite the rest), or nothing at all. An unknown state (receiver
// unreachable) is not an end: the session stays until its link expires.
func Ended(st Status, mark string) bool {
	switch st.State {
	case "", "UNKNOWN", "ERROR":
		return false
	}
	if st.Media != "" {
		return !strings.Contains(st.Media, mark)
	}
	return st.State == "IDLE"
}

type ccPlayer struct{ c *Chromecast }

func (p *ccPlayer) Load(ctx context.Context, m Media) error {
	return p.c.Load(ctx, m.URL, m.Type, m.Title, m.Kind == "image")
}
func (p *ccPlayer) Play(ctx context.Context) error            { return p.c.Play(ctx) }
func (p *ccPlayer) Pause(ctx context.Context) error           { return p.c.Pause(ctx) }
func (p *ccPlayer) Stop(ctx context.Context) error            { return p.c.Stop(ctx) }
func (p *ccPlayer) Seek(ctx context.Context, s float64) error { return p.c.Seek(ctx, s) }
func (p *ccPlayer) SetVolume(ctx context.Context, v int) error {
	return p.c.SetVolume(ctx, float64(max(0, min(100, v)))/100)
}
func (p *ccPlayer) Status(ctx context.Context) Status { return p.c.Status(ctx) }
func (p *ccPlayer) Close() error                      { return p.c.Close() }

type dlnaPlayer struct{ r *Receiver }

func (p *dlnaPlayer) Load(ctx context.Context, m Media) error {
	return p.r.DLNALoad(ctx, m.URL, m.Type, m.Title, m.Kind, m.Size)
}
func (p *dlnaPlayer) Play(ctx context.Context) error             { return p.r.DLNAPlay(ctx) }
func (p *dlnaPlayer) Pause(ctx context.Context) error            { return p.r.DLNAPause(ctx) }
func (p *dlnaPlayer) Stop(ctx context.Context) error             { return p.r.DLNAStop(ctx) }
func (p *dlnaPlayer) Seek(ctx context.Context, s float64) error  { return p.r.DLNASeek(ctx, s) }
func (p *dlnaPlayer) SetVolume(ctx context.Context, v int) error { return p.r.DLNASetVolume(ctx, v) }
func (p *dlnaPlayer) Status(ctx context.Context) Status          { return p.r.DLNAStatus(ctx) }
func (p *dlnaPlayer) Close() error                               { return nil }

// mediaTypes are the files that can be cast, with the type sent to receivers.
var mediaTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".mkv": "video/x-matroska", ".webm": "video/webm",
	".avi": "video/x-msvideo", ".ts": "video/mp2t", ".m2ts": "video/mp2t", ".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".wmv": "video/x-ms-wmv",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".opus": "audio/ogg", ".wav": "audio/wav", ".wma": "audio/x-ms-wma",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
}

// MediaType returns the MIME type and kind (video, audio, image) of a file
// name, or empty strings when it cannot be cast.
func MediaType(name string) (mimeType, kind string) {
	t := mediaTypes[strings.ToLower(path.Ext(name))]
	if t == "" {
		return "", ""
	}
	k, _, _ := strings.Cut(t, "/")
	return t, k
}
