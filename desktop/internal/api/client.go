// Package api is the client of the StrataOS sync API (docs/DEVELOPMENT.md,
// "Sync API"): pairing, changes, transfers and file operations, over HTTPS
// with a pinned certificate and a device token.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"strataos-sync/internal/ratelimit"
)

// Entry is a file or folder of the NAS (MTime in Unix milliseconds).
type Entry struct {
	Path    string `json:"path"`
	Dir     bool   `json:"dir,omitempty"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Version string `json:"version,omitempty"`
	Hash    string `json:"hash,omitempty"`
	ID      string `json:"id,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

// Changes is a page of changes.
type Changes struct {
	Cursor  string  `json:"cursor"`
	Reset   bool    `json:"reset"`
	More    bool    `json:"more"`
	Changes []Entry `json:"changes"`
}

// Error is an error answered by the NAS.
type Error struct {
	Status   int
	Msg      string
	Conflict bool
	Current  *Entry
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return http.StatusText(e.Status)
	}
	return e.Msg
}

// StatusOf returns the HTTP status of an error from the NAS (0: another error).
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// ConflictOf returns the conflict answered by the NAS (412), if err is one.
func ConflictOf(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e.Conflict {
		return e, true
	}
	return nil, false
}

// Client talks to one NAS.
type Client struct {
	Base     string // https://host:port
	Token    string // device token
	Pin      string // certificate fingerprint
	Agent    string
	Up, Down *ratelimit.Limiter
	hc       *http.Client
	lp       *http.Client // long polls
}

// New returns a client for base with that certificate pin.
func New(base, token, pin string) *Client {
	c := &Client{Base: strings.TrimRight(base, "/"), Token: token, Pin: pin, Agent: "StrataOS-Sync"}
	host := ""
	if u, err := url.Parse(c.Base); err == nil {
		host = u.Hostname()
	}
	transport := func(header time.Duration) *http.Transport {
		return &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:     tlsConfig(host, pin),
			TLSHandshakeTimeout: 15 * time.Second,
			// A NAS that stops answering is an error, not a hang.
			ResponseHeaderTimeout: header,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	c.hc = &http.Client{Transport: transport(2 * time.Minute)}
	c.lp = &http.Client{Transport: transport(90 * time.Second)} // long polls answer within 55 s
	return c
}

func (c *Client) req(ctx context.Context, method, p string, q url.Values, body io.Reader) (*http.Request, error) {
	u := c.Base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	r.Header.Set("User-Agent", c.Agent)
	return r, nil
}

// readErr turns an error answer into an *Error.
func readErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var v struct {
		Error    string `json:"error"`
		Conflict bool   `json:"conflict"`
		Current  *Entry `json:"current"`
	}
	_ = json.Unmarshal(b, &v)
	if v.Error == "" {
		v.Error = strings.TrimSpace(string(b))
		if len(v.Error) > 200 || v.Error == "" {
			v.Error = resp.Status
		}
	}
	return &Error{Status: resp.StatusCode, Msg: v.Error, Conflict: v.Conflict || resp.StatusCode == http.StatusPreconditionFailed, Current: v.Current}
}

// call sends JSON (in may be nil) and decodes the answer into out (may be nil).
func (c *Client) call(ctx context.Context, method, p string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := c.req(ctx, method, p, q, body)
	if err != nil {
		return err
	}
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	hc := c.hc
	if q.Get("wait") != "" {
		hc = c.lp
	}
	resp, err := hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readErr(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- Pairing and account ----

// PairRequest signs a computer in: with a one-time code, or a password
// (and a two-factor code when the account has one).
type PairRequest struct {
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Code        string `json:"code,omitempty"`
	PairingCode string `json:"pairingCode,omitempty"`
	DeviceName  string `json:"deviceName"`
	Model       string `json:"model,omitempty"`
	App         string `json:"app"`
	Kind        string `json:"kind"`
	OS          string `json:"os"`
}

// PairResult is the answer of a successful pairing.
type PairResult struct {
	Token  string `json:"token"`
	Device struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"device"`
	User struct {
		Username string `json:"username"`
		FullName string `json:"fullName"`
		Role     string `json:"role"`
	} `json:"user"`
	Server struct {
		Hostname string `json:"hostname"`
		Version  string `json:"version"`
	} `json:"server"`
}

// ErrTwoFactor: the account needs a two-factor code (or it was wrong).
var ErrTwoFactor = errors.New("two-factor code required")

// Pair signs this computer in and returns its token.
func (c *Client) Pair(ctx context.Context, req PairRequest) (PairResult, error) {
	req.Kind = "desktop"
	b, _ := json.Marshal(req)
	r, err := c.req(ctx, "POST", "/api/mobile/pair", nil, bytes.NewReader(b))
	if err != nil {
		return PairResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(r)
	if err != nil {
		return PairResult{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var v struct {
			Error     string `json:"error"`
			TwoFactor bool   `json:"twoFactor"`
		}
		_ = json.Unmarshal(data, &v)
		if v.TwoFactor {
			return PairResult{}, fmt.Errorf("%w: %s", ErrTwoFactor, v.Error)
		}
		if v.Error == "" {
			v.Error = resp.Status
		}
		return PairResult{}, &Error{Status: resp.StatusCode, Msg: v.Error}
	}
	var out PairResult
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return PairResult{}, errors.New("unexpected answer from the NAS")
	}
	return out, nil
}

// Info is GET /api/sync/info.
type Info struct {
	Hostname string   `json:"hostname"`
	Version  string   `json:"version"`
	Time     int64    `json:"time"`
	API      int      `json:"api"`
	Features []string `json:"features"`
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var v Info
	err := c.call(ctx, "GET", "/api/sync/info", nil, nil, &v)
	return v, err
}

// UpdateDevice reports the app version and system of this computer.
func (c *Client) UpdateDevice(ctx context.Context, app, osName string) error {
	return c.call(ctx, "PUT", "/api/mobile/device", nil, map[string]string{"app": app, "os": osName}, nil)
}

// SignOut revokes the token of this computer.
func (c *Client) SignOut(ctx context.Context) error {
	return c.call(ctx, "DELETE", "/api/mobile/device", nil, nil, nil)
}

// Share is a shared folder the user may follow.
type Share struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
	Recycle  bool   `json:"recycleBin"`
}

func (c *Client) Shares(ctx context.Context) ([]Share, error) {
	var v []Share
	err := c.call(ctx, "GET", "/api/sync/shares", nil, nil, &v)
	return v, err
}

// DirEntry is an entry of the file manager listing.
type DirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// List lists a NAS folder (for choosing folders).
func (c *Client) List(ctx context.Context, p string) ([]DirEntry, error) {
	var v struct {
		Entries []DirEntry `json:"entries"`
	}
	err := c.call(ctx, "GET", "/api/files", url.Values{"path": {p}}, nil, &v)
	return v.Entries, err
}

// ---- Changes ----

// Changes returns what changed under folder after cursor; wait > 0 holds
// the request until something changes (long poll).
func (c *Client) Changes(ctx context.Context, folder, cursor string, wait time.Duration) (Changes, error) {
	q := url.Values{"folder": {folder}, "cursor": {cursor}, "limit": {"2000"}}
	if wait > 0 {
		q.Set("wait", strconv.Itoa(int(wait/time.Second)))
	}
	var v Changes
	err := c.call(ctx, "GET", "/api/sync/changes", q, nil, &v)
	return v, err
}

// Hashes returns the SHA-256 of files (those the NAS could compute in time).
func (c *Client) Hashes(ctx context.Context, folder string, paths []string) (map[string]string, error) {
	var v struct {
		Hashes map[string]string `json:"hashes"`
	}
	err := c.call(ctx, "POST", "/api/sync/hashes", nil, map[string]any{"folder": folder, "paths": paths}, &v)
	return v.Hashes, err
}

// ---- Operations ----

func (c *Client) Mkdir(ctx context.Context, folder, rel string) (Entry, error) {
	var e Entry
	err := c.call(ctx, "POST", "/api/sync/mkdir", nil, map[string]string{"folder": folder, "path": rel}, &e)
	return e, err
}

func (c *Client) Move(ctx context.Context, folder, from, to, ifMatch string) (Entry, error) {
	var e Entry
	err := c.call(ctx, "POST", "/api/sync/move", nil, map[string]string{"folder": folder, "from": from, "to": to, "ifMatch": ifMatch}, &e)
	return e, err
}

// Delete removes a file (to the recycle bin when the share has one) known
// at version ifMatch, or an empty folder.
func (c *Client) Delete(ctx context.Context, folder, rel, ifMatch string) (recycled bool, err error) {
	var v struct {
		Recycled bool `json:"recycled"`
	}
	err = c.call(ctx, "POST", "/api/sync/delete", nil, map[string]string{"folder": folder, "path": rel, "ifMatch": ifMatch}, &v)
	return v.Recycled, err
}

// ---- Transfers ----

// ErrChanged: the file changed on the NAS during the transfer.
var ErrChanged = errors.New("the file changed on the NAS during the transfer")

// Download writes the file from offset to w, checking it is still at
// version (when given). It returns the version and size served.
func (c *Client) Download(ctx context.Context, folder, rel, version string, offset int64, w io.Writer) (string, int64, error) {
	r, err := c.req(ctx, "GET", "/api/sync/file", url.Values{"folder": {folder}, "path": {rel}}, nil)
	if err != nil {
		return "", 0, err
	}
	if offset > 0 {
		r.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if version != "" {
			r.Header.Set("If-Range", `"`+version+`"`)
		}
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", 0, readErr(resp)
	}
	got := resp.Header.Get("X-Strata-Version")
	if version != "" && got != version {
		return got, 0, ErrChanged
	}
	if offset > 0 && resp.StatusCode != http.StatusPartialContent {
		return got, 0, ErrChanged // the range was refused: start again
	}
	var src io.Reader = resp.Body
	if c.Down != nil {
		src = ratelimit.Reader(ctx, resp.Body, c.Down)
	}
	n, err := io.Copy(w, src)
	return got, n, err
}

// Upload describes a file to send.
type Upload struct {
	Folder   string // the NAS folder followed
	Rel      string // path of the file below it
	Size     int64
	MTime    int64  // Unix milliseconds
	IfMatch  string // version replaced ("" = must not exist, "*" = any)
	Body     io.ReaderAt
	ResumeID string           // upload started before (resumed when still there)
	OnID     func(id string)  // the upload ID, to resume after a restart
	Progress func(done int64) // bytes sent so far
}

// UploadResult is a finished upload.
type UploadResult struct {
	Entry Entry  `json:"entry"`
	ID    string `json:"id"`
}

const chunkSize = 8 << 20

// UploadFile sends a file with the resumable uploads, replacing the
// version given. A 412 conflict leaves the upload on the NAS, complete:
// Commit places it under another name.
func (c *Client) UploadFile(ctx context.Context, u Upload) (UploadResult, error) {
	dir := path.Dir(u.Rel)
	folder := u.Folder
	if dir != "." {
		folder = u.Folder + "/" + dir
	}
	id, off := "", int64(0)
	if u.ResumeID != "" {
		var v struct {
			Offset int64 `json:"offset"`
			Size   int64 `json:"size"`
		}
		if err := c.call(ctx, "GET", "/api/mobile/uploads/"+url.PathEscape(u.ResumeID), nil, nil, &v); err == nil && v.Size == u.Size {
			id, off = u.ResumeID, v.Offset
		}
	}
	if id == "" {
		var v struct {
			ID     string `json:"id"`
			Offset int64  `json:"offset"`
			Done   bool   `json:"done"`
			Entry  Entry  `json:"entry"`
		}
		err := c.call(ctx, "POST", "/api/mobile/uploads", nil, map[string]any{"folder": folder, "name": path.Base(u.Rel), "size": u.Size,
			"modTime": u.MTime, "sync": true, "replace": true, "ifMatch": u.IfMatch, "mkdirs": true}, &v)
		if err != nil {
			return UploadResult{}, err
		}
		if v.Done {
			return UploadResult{Entry: fixEntry(v.Entry, u.Rel), ID: v.ID}, nil
		}
		id, off = v.ID, v.Offset
		if u.OnID != nil {
			u.OnID(id)
		}
	}
	for {
		if u.Progress != nil {
			u.Progress(off)
		}
		n := min(int64(chunkSize), u.Size-off)
		var body io.Reader = io.NewSectionReader(u.Body, off, n)
		if c.Up != nil {
			body = ratelimit.Reader(ctx, body, c.Up)
		}
		r, err := c.req(ctx, "PATCH", "/api/mobile/uploads/"+url.PathEscape(id), nil, body)
		if err != nil {
			return UploadResult{}, err
		}
		r.ContentLength = n
		r.Header.Set("Upload-Offset", strconv.FormatInt(off, 10))
		r.Header.Set("Content-Type", "application/offset+octet-stream")
		resp, err := c.hc.Do(r)
		if err != nil {
			return UploadResult{ID: id}, err
		}
		if resp.StatusCode == http.StatusConflict && resp.Header.Get("Upload-Offset") != "" {
			// Out of step (a chunk partly written before): go on from there.
			off, _ = strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			err := readErr(resp)
			resp.Body.Close()
			return UploadResult{ID: id}, err
		}
		var v struct {
			Offset int64 `json:"offset"`
			Done   bool  `json:"done"`
			Entry  Entry `json:"entry"`
		}
		err = json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		if err != nil {
			return UploadResult{ID: id}, err
		}
		if v.Done {
			return UploadResult{Entry: fixEntry(v.Entry, u.Rel), ID: id}, nil
		}
		if v.Offset <= off {
			return UploadResult{ID: id}, errors.New("upload not progressing")
		}
		off = v.Offset
	}
}

// fixEntry gives an entry answered by an upload its path below the folder.
func fixEntry(e Entry, rel string) Entry {
	e.Path = rel
	return e
}

// Commit completes a finished upload again under another name or condition.
func (c *Client) Commit(ctx context.Context, id, name, ifMatch, rel string) (Entry, error) {
	var v struct {
		Entry Entry `json:"entry"`
	}
	err := c.call(ctx, "POST", "/api/mobile/uploads/"+url.PathEscape(id)+"/commit", nil, map[string]string{"name": name, "ifMatch": ifMatch}, &v)
	return fixEntry(v.Entry, rel), err
}

// CancelUpload forgets an upload.
func (c *Client) CancelUpload(ctx context.Context, id string) error {
	return c.call(ctx, "DELETE", "/api/mobile/uploads/"+url.PathEscape(id), nil, nil, nil)
}
