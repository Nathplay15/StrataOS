// StrataOS Sync keeps folders of a computer synchronized with folders of a
// StrataOS NAS. Without a command it runs the tray app (and its settings
// page); the commands run it headless:
//
//	strataos-sync [--config DIR] pair --server URL --code CODE [--fingerprint FP | --trust]
//	strataos-sync [--config DIR] pair --link 'strataos://pair?…'
//	strataos-sync [--config DIR] pair --server URL --user NAME --password-file FILE [--otp CODE]
//	strataos-sync [--config DIR] add --local DIR --remote /srv/strata/volume/Share[/sub] [--exclude a,b]
//	strataos-sync [--config DIR] remove ID | list | status | unpair
//	strataos-sync [--config DIR] sync-once [--confirm-deletes | --restore-deletes]
//	strataos-sync [--config DIR] daemon
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"strataos-sync/internal/api"
	"strataos-sync/internal/app"
	"strataos-sync/internal/config"
	"strataos-sync/internal/platform"
	"strataos-sync/internal/tray"
)

var version = "dev"

func main() {
	attachConsole()
	global := flag.NewFlagSet("strataos-sync", flag.ExitOnError)
	dir := global.String("config", "", "folder of the settings and state (default: the user's configuration folder)")
	background := global.Bool("background", false, "start in the tray without opening the settings (start at login)")
	showVersion := global.Bool("version", false, "print the version")
	global.Usage = usage
	_ = global.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("strataos-sync", version)
		return
	}
	if *dir == "" {
		*dir = config.DefaultDir()
	}
	store := &config.Store{Dir: *dir}
	args := global.Args()
	if len(args) == 0 {
		runGUI(store, *background)
		return
	}
	if err := runCommand(store, args[0], args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "strataos-sync:", err)
		var ce exitCode
		if errors.As(err, &ce) {
			os.Exit(int(ce))
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `StrataOS Sync `+version+`: two-way synchronization of folders with a StrataOS NAS.

Usage: strataos-sync [--config DIR] [command]

Without a command: the tray app (settings at the first launch).
Commands (headless):
  pair      --server URL (--code CODE | --user NAME --password-file FILE [--otp CODE]) [--fingerprint FP | --trust] [--name NAME]
            or --link 'strataos://pair?…' (copied from the NAS web interface)
  add       --local DIR --remote /srv/strata/VOLUME/SHARE[/SUBFOLDER] [--exclude sub1,sub2]
  remove    ID
  list      the synchronized folders
  status    the state of each folder
  sync-once synchronize until up to date, then exit [--confirm-deletes | --restore-deletes]
  daemon    synchronize continuously (Ctrl+C stops)
  unpair    sign this computer out of the NAS
`)
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func runCommand(store *config.Store, cmd string, args []string) error {
	a, err := app.New(store, version)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "pair":
		server := fs.String("server", "", "address of the NAS (https://nas.local:7443)")
		link := fs.String("link", "", "pairing link from the web interface")
		code := fs.String("code", "", "one-time pairing code from the web interface")
		user := fs.String("user", "", "user name (instead of a code)")
		passFile := fs.String("password-file", "", "file holding the password (- for standard input)")
		pass := fs.String("password", "", "password (visible in the process list: prefer --password-file)")
		otp := fs.String("otp", "", "two-factor code")
		fp := fs.String("fingerprint", "", "expected SHA-256 fingerprint of the NAS certificate")
		trust := fs.Bool("trust", false, "accept the certificate presented (prints its fingerprint)")
		name := fs.String("name", "", "name of this computer on the NAS")
		_ = fs.Parse(args)
		in := app.PairInput{URL: *server, Link: *link, Code: *code, Username: *user, Password: *pass, OTP: *otp, Fingerprint: *fp, DeviceName: *name}
		if *passFile != "" {
			var b []byte
			if *passFile == "-" {
				b, err = io.ReadAll(os.Stdin)
			} else {
				b, err = os.ReadFile(*passFile)
			}
			if err != nil {
				return err
			}
			in.Password = strings.TrimRight(string(b), "\r\n")
		}
		if in.Link != "" {
			base, lfp, lcode, _, err := app.ParseLink(in.Link)
			if err != nil {
				return err
			}
			in.URL, in.Code, in.Link = base, lcode, ""
			if lfp != "" {
				in.Fingerprint = lfp
			}
		}
		base, err := app.NormalizeURL(in.URL)
		if err != nil {
			return err
		}
		in.URL = base
		presented, trusted, err := api.ProbeCertificate(ctx, base)
		if err != nil {
			return fmt.Errorf("cannot reach %s: %w", base, err)
		}
		switch {
		case trusted && in.Fingerprint == "":
		case in.Fingerprint != "" && api.NormalizeFingerprint(in.Fingerprint) != presented && !trusted:
			return fmt.Errorf("the NAS presents the certificate %s, not the one expected: pairing refused", presented)
		case in.Fingerprint == "" && !*trust:
			return fmt.Errorf("the NAS certificate is not signed by a public authority: check its fingerprint %s in the web interface, then pass --fingerprint (or --trust)", presented)
		case in.Fingerprint == "":
			in.Fingerprint = presented
			fmt.Println("certificate accepted:", presented)
		}
		if trusted {
			in.Fingerprint = ""
		}
		if err := a.Pair(ctx, in); err != nil {
			return err
		}
		c := a.Config()
		fmt.Printf("paired with %s (%s) as %s\n", c.NASName, c.Server, c.User)
		return nil

	case "unpair":
		if err := a.SignOut(ctx); err != nil {
			return err
		}
		fmt.Println("signed out")
		return nil

	case "add":
		local := fs.String("local", "", "local folder")
		remote := fs.String("remote", "", "NAS folder")
		exclude := fs.String("exclude", "", "subfolders left out, comma-separated")
		_ = fs.Parse(args)
		if err := a.Start(ctx); err != nil {
			return err
		}
		defer a.Stop()
		p, err := a.AddPair(*local, *remote)
		if err != nil {
			return err
		}
		if *exclude != "" {
			if err := a.UpdatePair(p.ID, nil, strings.Split(*exclude, ",")); err != nil {
				return err
			}
		}
		fmt.Println(p.ID)
		return nil

	case "remove":
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("remove ID (see list)")
		}
		return a.RemovePair(fs.Arg(0))

	case "list":
		for _, p := range a.Config().Pairs {
			paused := ""
			if p.Paused {
				paused = " (paused)"
			}
			fmt.Printf("%s  %s  <->  %s%s\n", p.ID, p.Local, p.Remote, paused)
		}
		return nil

	case "status", "sync-once":
		confirm := fs.Bool("confirm-deletes", false, "apply deletions held by the safety brake")
		restore := fs.Bool("restore-deletes", false, "restore the files the safety brake held instead of deleting them")
		_ = fs.Parse(args)
		if !a.Paired() {
			return errors.New("not paired: run pair first")
		}
		if err := a.Start(ctx); err != nil {
			return err
		}
		defer a.Stop()
		eng := a.Engine()
		eng.Pause(false)
		if cmd == "status" {
			for _, p := range eng.Status().Pairs {
				fmt.Printf("%s  %s <-> %s\n", p.ID, p.Local, p.Remote)
			}
			return nil
		}
		err := eng.SyncOnce(ctx)
		braked := false
		for _, p := range eng.Status().Pairs {
			if p.Brake != nil {
				braked = true
				if *confirm || *restore {
					eng.ResolveBrake(p.ID, *confirm)
				}
			}
		}
		if braked && (*confirm || *restore) {
			err = eng.SyncOnce(ctx)
			braked = false
			for _, p := range eng.Status().Pairs {
				braked = braked || p.Brake != nil
			}
		}
		for _, p := range eng.Status().Pairs {
			fmt.Printf("%s  %s: %s, %d files", p.ID, p.Local, p.State, p.Files)
			if p.Conflicts > 0 {
				fmt.Printf(", %d conflict(s)", p.Conflicts)
			}
			if p.Brake != nil {
				fmt.Printf(", %d deletions held (run again with --confirm-deletes or --restore-deletes)", p.Brake.LocalDeletes+p.Brake.RemoteDeletes)
			}
			for _, s := range p.Skipped {
				fmt.Printf("\n    skipped %s: %s", s.Path, s.Reason)
			}
			fmt.Println()
		}
		if err != nil {
			return err
		}
		if braked {
			return exitCode(3)
		}
		return nil

	case "daemon":
		_ = fs.Parse(args)
		if !a.Paired() {
			return errors.New("not paired: run pair first")
		}
		if err := a.Start(ctx); err != nil {
			return err
		}
		log.Printf("synchronizing %d folder(s); Ctrl+C stops", len(a.Config().Pairs))
		<-ctx.Done()
		a.Stop()
		return nil
	}
	usage()
	return exitCode(2)
}

// runGUI runs the tray app.
func runGUI(store *config.Store, background bool) {
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		log.Fatal(err)
	}
	urlFile := filepath.Join(store.Dir, "ui-url")
	// One app per account: a second launch opens the settings of the first.
	if b, err := os.ReadFile(urlFile); err == nil {
		u := strings.TrimSpace(string(b))
		hc := &http.Client{Timeout: 2 * time.Second}
		if resp, err := hc.Get(u); err == nil {
			resp.Body.Close()
			if !background {
				_ = platform.Open(u)
			}
			return
		}
	}
	setupLog(store.LogFile())
	a, err := app.New(store, version)
	if err != nil {
		log.Fatal(err)
	}
	a.Notify = func(title, body string) {
		if err := platform.Notify(title, body); err != nil {
			log.Printf("notification: %v", err)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a.FirstRun()
	if err := a.Start(ctx); err != nil {
		log.Printf("start: %v", err)
	}
	ui, err := a.ServeUI(ctx)
	if err != nil {
		log.Fatal(err)
	}
	_ = os.WriteFile(urlFile, []byte(ui.URL()), 0o600)
	defer os.Remove(urlFile)
	open := func() {
		if err := platform.Open(ui.URL()); err != nil {
			log.Printf("open the settings: %v", err)
		}
	}
	if !background || !a.Paired() || !tray.Available {
		go func() {
			time.Sleep(300 * time.Millisecond)
			open()
		}()
	}
	log.Printf("StrataOS Sync %s started (settings: %s)", version, strings.Split(ui.URL(), "?")[0])
	tray.Run(a, open, ctx.Done(), func() {
		cancel()
		a.Stop()
	})
}

// setupLog writes the log to a file, started again above 5 MB.
func setupLog(file string) {
	if st, err := os.Stat(file); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(file, file+".old")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
}
