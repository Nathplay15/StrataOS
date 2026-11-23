package api

import (
	"context"
	"fmt"
	"net/url"

	"strataos/internal/docker"
	"strataos/internal/lxc"
	"strataos/internal/scheduler"
	"strataos/internal/vms"
)

// Scheduled actions (roadmap 0.15, "scheduled actions in plain language"):
// a schedule of type "action" starts, stops or restarts a VM, an LXC
// container or a Docker container at its cron time.

func init() {
	scheduler.ActionRunner = runScheduledAction
}

// runScheduledAction performs one action on one guest.
func runScheduledAction(ctx context.Context, kind, target, action string, logf func(string, ...any)) (string, error) {
	switch kind {
	case "vm":
		a, ok := map[string]string{"start": "start", "stop": "shutdown", "restart": "reboot"}[action]
		if !ok {
			return "", fmt.Errorf("unknown action %q", action)
		}
		logf("VM %s: %s…", target, a)
		if err := vms.Action(target, a); err != nil {
			return "", err
		}
		return fmt.Sprintf("VM %s: %s done", target, a), nil
	case "lxc":
		switch action {
		case "start":
			logf("Container %s: start…", target)
			if err := lxc.Start(ctx, target); err != nil {
				return "", err
			}
		case "stop":
			logf("Container %s: stop…", target)
			if err := lxc.Stop(target); err != nil {
				return "", err
			}
		case "restart":
			logf("Container %s: restart…", target)
			if err := lxc.Stop(target); err != nil {
				return "", err
			}
			if err := lxc.Start(ctx, target); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Container %s: %s done", target, action), nil
	case "docker":
		if !docker.Available() {
			return "", fmt.Errorf("Docker Engine is not running")
		}
		logf("Container %s: %s…", target, action)
		c := docker.New()
		if err := c.Call(ctx, "POST", "/containers/"+url.PathEscape(target)+"/"+action, nil, nil, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("Container %s: %s done", target, action), nil
	}
	return "", fmt.Errorf("unknown target kind %q", kind)
}
