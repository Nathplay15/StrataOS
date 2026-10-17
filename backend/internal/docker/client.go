// Package docker talks to the Docker Engine API over its Unix socket.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const Socket = "/var/run/docker.sock"

type Client struct {
	http   *http.Client
	stream *http.Client
}

func New() *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", Socket)
	}
	return &Client{
		http:   &http.Client{Transport: &http.Transport{DialContext: dial, MaxIdleConns: 10}, Timeout: 60 * time.Second},
		stream: &http.Client{Transport: &http.Transport{DialContext: dial}},
	}
}

// Available reports whether the Docker socket is present.
func Available() bool {
	_, err := os.Stat(Socket)
	return err == nil
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s", e.Message) }

func (c *Client) do(ctx context.Context, cl *http.Client, method, path string, query url.Values, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker engine unreachable: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e struct {
			Message string `json:"message"`
		}
		data, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return nil, &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	return resp, nil
}

// Call performs a request and decodes the JSON response into out (may be nil).
func (c *Client) Call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.do(ctx, c.http, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Stream performs a request whose body is consumed by the caller.
func (c *Client) Stream(ctx context.Context, method, path string, query url.Values, body any) (io.ReadCloser, error) {
	resp, err := c.do(ctx, c.stream, method, path, query, body)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// maxLogLine bounds a log line forwarded by Demux: a container printing without
// newlines must not make the daemon buffer its output without limit.
const maxLogLine = 64 * 1024

// Demux copies a multiplexed stdout/stderr stream into w, line by line via fn.
func Demux(r io.Reader, tty bool, fn func(stream string, line string)) error {
	if tty {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), maxLogLine)
		for sc.Scan() {
			fn("stdout", sc.Text())
		}
		return sc.Err()
	}
	hdr := make([]byte, 8)
	var pending [3]string
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		idx := int(hdr[0])
		if idx > 2 {
			idx = 1
		}
		name := "stdout"
		if idx == 2 {
			name = "stderr"
		}
		// Read the frame in bounded chunks (the header announces up to 4 GiB).
		frame := io.LimitReader(r, int64(binary.BigEndian.Uint32(hdr[4:])))
		buf := make([]byte, 32*1024)
		for {
			n, err := frame.Read(buf)
			if n > 0 {
				lines := strings.Split(pending[idx]+string(buf[:n]), "\n")
				for _, l := range lines[:len(lines)-1] {
					fn(name, l)
				}
				rest := lines[len(lines)-1]
				for len(rest) >= maxLogLine { // flush over-long lines in pieces
					fn(name, rest[:maxLogLine])
					rest = rest[maxLogLine:]
				}
				pending[idx] = rest
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		// LimitReader hides a truncated frame: check the stream itself ended cleanly.
		if frame.(*io.LimitedReader).N > 0 {
			return nil
		}
	}
}

// Hijack starts an exec session and returns the raw bidirectional connection.
func (c *Client) Hijack(ctx context.Context, execID string) (net.Conn, *bufio.Reader, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", Socket)
	if err != nil {
		return nil, nil, err
	}
	body := `{"Detach":false,"Tty":true}`
	req := fmt.Sprintf("POST /exec/%s/start HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: %d\r\n\r\n%s", url.PathEscape(execID), len(body), body)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, nil, fmt.Errorf("exec start: %s", resp.Status)
	}
	return conn, br, nil
}
