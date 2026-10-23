// Package console is the appliance screen of StrataOS on the machine's own
// display (tty1): where to open the web interface, a QR code of the address,
// health, and a few local actions for whoever stands in front of the NAS.
// It talks to the daemon over a unix socket that only root can open.
package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Socket is where the daemon serves the console; the daemon also checks that
// the peer is root (SO_PEERCRED).
const Socket = "/run/strata/console.sock"

// Status is what the screen shows, refreshed every few seconds.
type Status struct {
	Hostname  string   `json:"hostname"`
	Version   string   `json:"version"`
	SetupDone bool     `json:"setupDone"`
	HTTPPort  string   `json:"httpPort"`
	HTTPSPort string   `json:"httpsPort"`
	Alerts    []Alert  `json:"alerts"`
	Volumes   []Volume `json:"volumes"`
	// Recovery: administrator recovery codes may be generated.
	Recovery bool `json:"recovery"`
	// NetworkDeadline is set while a network reset made from the console
	// waits to be kept.
	NetworkDeadline *time.Time `json:"networkDeadline,omitempty"`
}

type Alert struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type Volume struct {
	Name   string `json:"name"`
	Used   int64  `json:"used"`
	Total  int64  `json:"total"`
	Health string `json:"health"`
}

// Iface is a network interface for the details page.
type Iface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	State     string   `json:"state"`
	Addresses []string `json:"addresses"`
	Speed     int      `json:"speed"`
	Kind      string   `json:"kind"`
}

type Network struct {
	Interfaces []Iface  `json:"interfaces"`
	Gateway    string   `json:"gateway"`
	DNS        []string `json:"dns"`
}

// Code is a recovery code as the daemon hands it to the console.
type Code struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// Pending is a network reset waiting for the person at the console to keep
// it (the daemon holds its confirmation token).
type Pending struct {
	Deadline time.Time `json:"deadline"`
}

// Client calls the daemon over its console socket.
type Client struct {
	http *http.Client
}

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

func (c *Client) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://console"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Status() (*Status, error) {
	var s Status
	return &s, c.call("GET", "/status", nil, &s)
}

func (c *Client) Network() (*Network, error) {
	var n Network
	return &n, c.call("GET", "/network", nil, &n)
}

func (c *Client) RecoveryCode() (*Code, error) {
	var k Code
	return &k, c.call("POST", "/recovery", nil, &k)
}

func (c *Client) ResetNetwork() (*Pending, error) {
	var p Pending
	return &p, c.call("POST", "/network/reset", nil, &p)
}

func (c *Client) KeepNetwork() error { return c.call("POST", "/network/keep", nil, nil) }

func (c *Client) UndoNetwork() error { return c.call("POST", "/network/undo", nil, nil) }

// Power asks the daemon to "reboot" or "poweroff" (logged as a console action).
func (c *Client) Power(action string) error { return c.call("POST", "/power/"+action, nil, nil) }
