package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type PortMap struct {
	Host      int    `json:"host"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol"` // tcp | udp
	HostIP    string `json:"hostIp"`
}

type VolumeMap struct {
	Source   string `json:"source"` // host path or named volume
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

type CreateReq struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Ports      []PortMap         `json:"ports"`
	Volumes    []VolumeMap       `json:"volumes"`
	Env        map[string]string `json:"env"`
	Restart    string            `json:"restart"` // no | always | unless-stopped | on-failure
	Network    string            `json:"network"`
	Command    []string          `json:"command"`
	Privileged bool              `json:"privileged"`
	MemoryMB   int64             `json:"memoryMb"`
	CPUs       float64           `json:"cpus"`
	Labels     map[string]string `json:"labels"`
	Devices    []string          `json:"devices"`
	Start      bool              `json:"start"`
}

var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// Create builds and creates a container from a simplified request.
func (c *Client) Create(ctx context.Context, r CreateReq) (string, error) {
	if r.Image == "" {
		return "", fmt.Errorf("image is required")
	}
	if r.Name != "" && !containerNameRe.MatchString(r.Name) {
		return "", fmt.Errorf("invalid container name")
	}
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range r.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if p.Container <= 0 || p.Container > 65535 || p.Host < 0 || p.Host > 65535 {
			return "", fmt.Errorf("invalid port mapping")
		}
		key := fmt.Sprintf("%d/%s", p.Container, proto)
		exposed[key] = struct{}{}
		if p.Host > 0 {
			bindings[key] = append(bindings[key], map[string]string{"HostIp": p.HostIP, "HostPort": strconv.Itoa(p.Host)})
		}
	}
	var binds []string
	for _, v := range r.Volumes {
		if v.Source == "" || v.Target == "" {
			continue
		}
		b := v.Source + ":" + v.Target
		if v.ReadOnly {
			b += ":ro"
		}
		binds = append(binds, b)
	}
	var env []string
	for k, v := range r.Env {
		env = append(env, k+"="+v)
	}
	restart := r.Restart
	if restart == "" {
		restart = "unless-stopped"
	}
	var devices []map[string]string
	for _, d := range r.Devices {
		devices = append(devices, map[string]string{"PathOnHost": d, "PathInContainer": d, "CgroupPermissions": "rwm"})
	}
	host := map[string]any{
		"PortBindings":  bindings,
		"Binds":         binds,
		"RestartPolicy": map[string]any{"Name": restart},
		"Privileged":    r.Privileged,
		"Devices":       devices,
	}
	if r.Network != "" {
		host["NetworkMode"] = r.Network
	}
	if r.MemoryMB > 0 {
		host["Memory"] = r.MemoryMB * 1024 * 1024
	}
	if r.CPUs > 0 {
		host["NanoCpus"] = int64(r.CPUs * 1e9)
	}
	labels := map[string]string{"io.strataos.managed": "true"}
	for k, v := range r.Labels {
		labels[k] = v
	}
	body := map[string]any{
		"Image":        r.Image,
		"Env":          env,
		"ExposedPorts": exposed,
		"HostConfig":   host,
		"Labels":       labels,
	}
	if len(r.Command) > 0 {
		body["Cmd"] = r.Command
	}
	q := url.Values{}
	if r.Name != "" {
		q.Set("name", r.Name)
	}
	var res struct {
		ID string `json:"Id"`
	}
	err := c.Call(ctx, "POST", "/containers/create", q, body, &res)
	if apiErr, ok := err.(*APIError); ok && apiErr.Status == 404 {
		// Image missing locally: pull then retry.
		if err := c.Pull(ctx, r.Image, func(string, float64) {}); err != nil {
			return "", err
		}
		err = c.Call(ctx, "POST", "/containers/create", q, body, &res)
	}
	if err != nil {
		return "", err
	}
	if r.Start {
		if err := c.Call(ctx, "POST", "/containers/"+res.ID+"/start", nil, nil, nil); err != nil {
			return res.ID, err
		}
	}
	return res.ID, nil
}

// Pull pulls an image, reporting progress lines and an overall percentage.
func (c *Client) Pull(ctx context.Context, image string, progress func(line string, pct float64)) error {
	name, tag := image, "latest"
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i:], "/") {
		name, tag = image[:i], image[i+1:]
	}
	if i := strings.Index(name, "@"); i > 0 {
		tag = ""
	}
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	body, err := c.Stream(ctx, "POST", "/images/create", q, nil)
	if err != nil {
		return err
	}
	defer body.Close()
	dec := json.NewDecoder(body)
	layers := map[string][2]float64{}
	for {
		var m struct {
			Status         string `json:"status"`
			ID             string `json:"id"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current float64 `json:"current"`
				Total   float64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("%s", m.Error)
		}
		if m.ProgressDetail.Total > 0 && m.Status == "Downloading" {
			layers[m.ID] = [2]float64{m.ProgressDetail.Current, m.ProgressDetail.Total}
		}
		if m.Status == "Download complete" || m.Status == "Pull complete" || m.Status == "Already exists" {
			if l, ok := layers[m.ID]; ok {
				layers[m.ID] = [2]float64{l[1], l[1]}
			}
		}
		var cur, tot float64
		for _, l := range layers {
			cur += l[0]
			tot += l[1]
		}
		pct := -1.0
		if tot > 0 {
			pct = cur / tot * 100
		}
		line := m.Status
		if m.ID != "" {
			line = m.ID + ": " + m.Status
		}
		if m.Status != "Downloading" && m.Status != "Extracting" && m.Status != "Waiting" {
			progress(line, pct)
		} else {
			progress("", pct)
		}
	}
}

type Stats struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsage   int64   `json:"memUsage"`
	MemLimit   int64   `json:"memLimit"`
	NetRx      int64   `json:"netRx"`
	NetTx      int64   `json:"netTx"`
	BlockRead  int64   `json:"blockRead"`
	BlockWrite int64   `json:"blockWrite"`
	PIDs       int64   `json:"pids"`
}

type rawStats struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage int64            `json:"usage"`
		Limit int64            `json:"limit"`
		Stats map[string]int64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes int64 `json:"rx_bytes"`
		TxBytes int64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value int64  `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current int64 `json:"current"`
	} `json:"pids_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage float64 `json:"total_usage"`
	} `json:"cpu_usage"`
	SystemUsage float64 `json:"system_cpu_usage"`
	OnlineCPUs  float64 `json:"online_cpus"`
}

func (c *Client) Stats(ctx context.Context, id string) (*Stats, error) {
	var r rawStats
	if err := c.Call(ctx, "GET", "/containers/"+url.PathEscape(id)+"/stats", url.Values{"stream": {"false"}}, nil, &r); err != nil {
		return nil, err
	}
	s := &Stats{MemLimit: r.MemoryStats.Limit, PIDs: r.PidsStats.Current}
	mem := r.MemoryStats.Usage
	if v, ok := r.MemoryStats.Stats["inactive_file"]; ok && v < mem {
		mem -= v
	}
	s.MemUsage = mem
	cpuDelta := r.CPUStats.CPUUsage.TotalUsage - r.PreCPUStats.CPUUsage.TotalUsage
	sysDelta := r.CPUStats.SystemUsage - r.PreCPUStats.SystemUsage
	if sysDelta > 0 && cpuDelta > 0 {
		n := r.CPUStats.OnlineCPUs
		if n == 0 {
			n = 1
		}
		s.CPUPercent = cpuDelta / sysDelta * n * 100
	}
	for _, n := range r.Networks {
		s.NetRx += n.RxBytes
		s.NetTx += n.TxBytes
	}
	for _, b := range r.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			s.BlockRead += b.Value
		case "write":
			s.BlockWrite += b.Value
		}
	}
	return s, nil
}

// Exec creates an interactive exec instance and returns its ID.
func (c *Client) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	body := map[string]any{"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true, "Cmd": cmd, "Env": []string{"TERM=xterm-256color"}}
	var res struct {
		ID string `json:"Id"`
	}
	if err := c.Call(ctx, "POST", "/containers/"+url.PathEscape(id)+"/exec", nil, body, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

func (c *Client) ExecResize(ctx context.Context, execID string, cols, rows int) error {
	q := url.Values{"h": {strconv.Itoa(rows)}, "w": {strconv.Itoa(cols)}}
	return c.Call(ctx, "POST", "/exec/"+url.PathEscape(execID)+"/resize", q, nil, nil)
}
