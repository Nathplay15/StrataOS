package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"

	"strataos-sync/internal/agent"
	"strataos-sync/internal/config"
	"strataos-sync/internal/platform"
)

var accountRe = regexp.MustCompile(`^[a-z0-9]{1,24}$`)

func validAccount(id string) bool { return accountRe.MatchString(id) }

// runAgent runs the sync engine for the desktop app: it prints
// {"agent":1,"port":N} on its first line, then serves the API until its
// standard input closes (the app quit or died) or it gets a signal.
func runAgent(dir string, args []string) {
	token := os.Getenv("STRATAOS_AGENT_TOKEN")
	os.Unsetenv("STRATAOS_AGENT_TOKEN")
	dev := os.Getenv("STRATAOS_AGENT_DEV") == "1"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
	setupLog(filepath.Join(dir, "strataos-sync.log"))
	if len(token) < 32 {
		log.Fatal("STRATAOS_AGENT_TOKEN missing: the agent is started by the desktop app")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	removeLegacyAutostart(dir)
	a, err := agent.New(ctx, dir, version, token)
	if err != nil {
		log.Fatal(err)
	}
	a.Dev = dev
	port, err := a.Listen()
	if err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"agent": 1, "port": port, "version": version})
	fmt.Println(string(b))
	log.Printf("StrataOS Sync agent %s on 127.0.0.1:%d", version, port)
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	<-ctx.Done()
	a.Stop()
}

// removeLegacyAutostart: the tray app of 0.10 started itself at login;
// the desktop app now does (its own entry), so the old entry goes once.
func removeLegacyAutostart(dir string) {
	st := &config.Store{Dir: dir}
	c, err := st.Load()
	if err != nil || !c.Autostart {
		return
	}
	if platform.Autostart() {
		if err := platform.SetAutostart(false); err != nil {
			log.Printf("start at login of the former app: %v", err)
			return
		}
	}
	_, _ = st.Update(func(c *config.Config) error { c.Autostart = false; return nil })
}

// listAccounts prints the accounts of the desktop app.
func listAccounts(dir string) {
	show := func(id, d string) {
		c, err := (&config.Store{Dir: d}).Load()
		if err != nil || !c.Paired() {
			return
		}
		fmt.Printf("%s  %s (%s) as %s, %d folder(s)\n", id, c.NASName, c.Server, c.User, len(c.Pairs))
	}
	show(agent.MainAccount, dir)
	ents, _ := os.ReadDir(filepath.Join(dir, "accounts"))
	for _, e := range ents {
		if e.IsDir() && validAccount(e.Name()) {
			show(e.Name(), filepath.Join(dir, "accounts", e.Name()))
		}
	}
}

// launchDesktopApp starts the desktop app installed next to this program
// (a start at login written by the former tray app runs this).
func launchDesktopApp(background bool) {
	exe := platform.Executable()
	d := filepath.Dir(exe)
	var cands []string
	switch runtime.GOOS {
	case "windows":
		cands = []string{filepath.Join(d, "strataos-desktop.exe"), filepath.Join(d, "StrataOS.exe")}
	case "darwin":
		cands = []string{filepath.Join(d, "strataos-desktop")}
	default:
		cands = []string{filepath.Join(d, "strataos-desktop"), "/usr/bin/strataos-desktop"}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			args := []string{}
			if background {
				args = append(args, "--background")
			}
			cmd := exec.Command(c, args...)
			if err := cmd.Start(); err == nil {
				return
			}
		}
	}
	usage()
	os.Exit(2)
}
