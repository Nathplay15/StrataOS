package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"strataos/internal/jobs"
	"strataos/internal/vms"
)

func (s *Server) vmRoutes() {
	s.admin("GET /api/vms", func(r *http.Request) (any, error) {
		out := map[string]any{"installed": vms.Installed(), "kvm": vms.KVM()}
		if !vms.Installed() {
			out["vms"] = []vms.VM{}
			return out, nil
		}
		list, err := vms.List()
		if err != nil {
			return nil, err
		}
		s.decorateVMs(list)
		out["vms"] = list
		out["stats"] = vms.Stats()
		var bridges []string
		entries, _ := os.ReadDir("/sys/class/net")
		for _, e := range entries {
			if _, err := os.Stat("/sys/class/net/" + e.Name() + "/bridge"); err == nil && !strings.HasPrefix(e.Name(), "virbr") && !strings.HasPrefix(e.Name(), "docker") && !strings.HasPrefix(e.Name(), "br-") {
				bridges = append(bridges, e.Name())
			}
		}
		out["bridges"] = bridges
		return out, nil
	})

	s.admin("GET /api/vms/isos", func(r *http.Request) (any, error) {
		list := vms.ISOs()
		if list == nil {
			list = []string{}
		}
		return list, nil
	})

	s.admin("POST /api/vms", func(r *http.Request) (any, error) {
		var req vms.CreateReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := vms.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Creating virtual machine "+req.Name)
		return s.jobs.Start("vm", "Creating VM "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if err := vms.Create(ctx, req, j.Logf); err != nil {
				return err
			}
			if req.Autostart {
				return s.setVMAutostart(req.Name, true, 0, 0)
			}
			return nil
		}), nil
	})

	s.admin("POST /api/vms/{name}/{action}", func(r *http.Request) (any, error) {
		name, action := r.PathValue("name"), r.PathValue("action")
		var req struct {
			ISO       string `json:"iso"`
			Autostart bool   `json:"autostart"`
			VCPUs     int    `json:"vcpus"`
			MemoryMB  int    `json:"memoryMb"`
			Order     int    `json:"order"`
			Delay     int    `json:"delay"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var err error
		switch action {
		case "media":
			err = vms.SetISO(name, req.ISO)
		case "autostart":
			err = s.setVMAutostart(name, req.Autostart, req.Order, req.Delay)
		case "resources":
			err = vms.Update(name, req.VCPUs, req.MemoryMB)
		default:
			err = vms.Action(name, action)
		}
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", fmt.Sprintf("VM %s: %s", name, action))
		return nil, nil
	})

	s.admin("DELETE /api/vms/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := vms.Delete(name, r.URL.Query().Get("disks") == "1"); err != nil {
			return nil, badRequest(err.Error())
		}
		_ = s.setVMAutostart(name, false, 0, 0)
		s.audit(r, "vms", "Deleted virtual machine "+name)
		return nil, nil
	})

	// VNC console: the browser (noVNC) talks RFB over a websocket that we relay to
	// the VM's VNC server, which only listens on 127.0.0.1.
	s.adminRaw("GET /api/vms/{name}/vnc", func(w http.ResponseWriter, r *http.Request) {
		vm, err := vms.Get(r.PathValue("name"))
		if err != nil || vm.VNC <= 0 {
			writeErr(w, notFound("console unavailable (is the VM running?)"))
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		tcp, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", vm.VNC), 5*time.Second)
		if err != nil {
			c.Close(websocket.StatusInternalError, "VNC unreachable")
			return
		}
		defer tcp.Close()
		ws := websocket.NetConn(ctx, c, websocket.MessageBinary)
		s.audit(r, "vms", "Opened the console of "+vm.Name)
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(ws, tcp); done <- struct{}{} }()
		go func() { _, _ = io.Copy(tcp, ws); done <- struct{}{} }()
		<-done
	})

	s.vmMoreRoutes()
}
